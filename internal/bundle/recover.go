package bundle

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/kube"
)

// resetPasswordName names both the one-shot job and the secret feeding it.
const resetPasswordName = "skali-reset-password"

// DeployedSkalidImage reads the image the running skalid deployment uses,
// so one-shot operator jobs run the exact daemon build that owns the
// database schema.
func DeployedSkalidImage(ctx context.Context, client *kube.Client) (string, error) {
	deployment, err := client.Clientset.AppsV1().Deployments(Namespace).Get(ctx, "skalid", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read the skalid deployment: %w", err)
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "skalid" {
			return container.Image, nil
		}
	}
	return "", errors.New("the skalid deployment has no skalid container")
}

// ResetUserPassword runs `skalid user reset-password` as a one-shot job in
// the platform namespace: the operator recovery path when the only admin is
// locked out, needing nothing but the node's kubeconfig. The password
// travels through a secret that is deleted with the job once it finished,
// whichever way it finished; nothing about the reset persists in-cluster.
func ResetUserPassword(ctx context.Context, client *kube.Client, image, email, password string,
	disableTwoFactor bool, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	if image == "" || email == "" || password == "" {
		return errors.New("bundle: image, email, and password are required to reset a password")
	}
	objects, err := ParseManifest([]byte(resetPasswordYAML(image, email, password, disableTwoFactor)))
	if err != nil {
		return err
	}
	applier := &Applier{Client: client}
	progress.Start("Reset password")
	// A previous run leaves its completed job behind only when cleanup
	// was interrupted; the pod template is immutable, so it must go
	// before the new one applies.
	if err := deleteResetPasswordObjects(ctx, client); err != nil {
		return err
	}
	if err := applier.wait(ctx, "removal of job "+resetPasswordName, func(ctx context.Context) (bool, error) {
		_, err := client.Clientset.BatchV1().Jobs(Namespace).Get(ctx, resetPasswordName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}); err != nil {
		return err
	}
	defer func() {
		_ = deleteResetPasswordObjects(context.WithoutCancel(ctx), client)
	}()
	if err := applier.ApplyObjects(ctx, objects); err != nil {
		return err
	}
	if err := applier.WaitJobComplete(ctx, Namespace, resetPasswordName); err != nil {
		if detail := resetPasswordFailure(ctx, client); detail != "" {
			return fmt.Errorf("bundle: password reset failed: %s", detail)
		}
		return err
	}
	progress.Done(email)
	return nil
}

// deleteResetPasswordObjects removes the job (with its pods) and the secret;
// absent objects are fine.
func deleteResetPasswordObjects(ctx context.Context, client *kube.Client) error {
	propagation := metav1.DeletePropagationForeground
	err := client.Clientset.BatchV1().Jobs(Namespace).Delete(ctx, resetPasswordName,
		metav1.DeleteOptions{PropagationPolicy: &propagation})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete job %s: %w", resetPasswordName, err)
	}
	err = client.Clientset.CoreV1().Secrets(Namespace).Delete(ctx, resetPasswordName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete secret %s: %w", resetPasswordName, err)
	}
	return nil
}

// resetPasswordFailure returns the last line the failed job's pod printed,
// which is skalid's own error ("no user with email ..."); empty when no
// log could be read.
func resetPasswordFailure(ctx context.Context, client *kube.Client) string {
	pods, err := client.Clientset.CoreV1().Pods(Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + resetPasswordName,
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	pod := pods.Items[len(pods.Items)-1]
	logs, err := client.Clientset.CoreV1().Pods(Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(logs)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func resetPasswordYAML(image, email, password string, disableTwoFactor bool) string {
	flags := ""
	if disableTwoFactor {
		flags = "--disable-2fa"
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %[1]s
  namespace: %[2]s
type: Opaque
data:
  RESET_EMAIL: %[4]s
  RESET_PASSWORD: %[5]s
---
apiVersion: batch/v1
kind: Job
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: reset
          image: %[3]s
          imagePullPolicy: IfNotPresent
          command:
            - sh
            - -c
            - >
              printf '%%s' "$RESET_PASSWORD" |
              skalid user reset-password --email "$RESET_EMAIL" --password-stdin %[6]s
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: skali-db-app
                  key: uri
            - name: RESET_EMAIL
              valueFrom:
                secretKeyRef:
                  name: %[1]s
                  key: RESET_EMAIL
            - name: RESET_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: %[1]s
                  key: RESET_PASSWORD
`, resetPasswordName, Namespace, image,
		base64.StdEncoding.EncodeToString([]byte(email)),
		base64.StdEncoding.EncodeToString([]byte(password)),
		flags)
}
