package installer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
)

// BundleInventory lists what removing the Skali bundle destroys: every
// project namespace, the platform namespace, the system namespace with its
// databases and registry contents, and the operator namespaces.
type BundleInventory struct {
	ProjectNamespaces []string
	SystemNamespaces  []string
}

// GatherBundleInventory reads what a bundle uninstall would delete.
func GatherBundleInventory(ctx context.Context, client *kube.Client) (*BundleInventory, error) {
	inventory := &BundleInventory{}
	projects, err := client.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: kubernetes.ManagedSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("list project namespaces: %w", err)
	}
	for _, namespace := range projects.Items {
		inventory.ProjectNamespaces = append(inventory.ProjectNamespaces, namespace.Name)
	}
	sort.Strings(inventory.ProjectNamespaces)
	inventory.SystemNamespaces = append([]string{"skali-platform", bundle.Namespace}, bundle.OperatorNamespaces...)
	return inventory, nil
}

// UninstallBundle removes Skali and all project workloads and data from
// the cluster, keeping bare k3s running. Deletion order: project
// namespaces, the platform namespace, skali-system, then the operator
// namespaces; each wave waits for termination. The record survives with
// its bundle version cleared, so the host re-detects as an uninitialized
// managed server.
func UninstallBundle(ctx context.Context, runner host.Runner, client *kube.Client, record *Record, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	inventory, err := GatherBundleInventory(ctx, client)
	if err != nil {
		return err
	}

	waves := [][]string{
		inventory.ProjectNamespaces,
		{"skali-platform"},
		{bundle.Namespace},
		bundle.OperatorNamespaces,
	}
	titles := []string{
		"Remove project namespaces",
		"Remove platform namespace",
		"Remove skali-system",
		"Remove blessed operators",
	}
	for index, wave := range waves {
		progress.Start(titles[index])
		deleted, err := deleteNamespaces(ctx, client, wave)
		if err != nil {
			return err
		}
		if deleted == 0 {
			progress.Skip("nothing to remove")
			continue
		}
		progress.Done(fmt.Sprintf("%d namespace(s)", deleted))
	}

	progress.Start("Update " + RecordPath)
	record.Versions.Bundle = ""
	if err := SaveRecord(ctx, runner, record); err != nil {
		return err
	}
	progress.Done("")
	return nil
}

// deleteNamespaces deletes the named namespaces and waits for termination;
// absent namespaces are skipped.
func deleteNamespaces(ctx context.Context, client *kube.Client, names []string) (int, error) {
	deleted := 0
	for _, name := range names {
		err := client.Clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return deleted, fmt.Errorf("delete namespace %s: %w", name, err)
		}
		deleted++
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		remaining := 0
		for _, name := range names {
			_, err := client.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				remaining++
			} else if !apierrors.IsNotFound(err) {
				return deleted, fmt.Errorf("wait for namespace %s: %w", name, err)
			}
		}
		if remaining == 0 {
			return deleted, nil
		}
		if time.Now().After(deadline) {
			return deleted, fmt.Errorf("timed out waiting for %d namespace(s) to terminate", remaining)
		}
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// UninstallNode removes k3s and the skali state from this host. Single
// node only in this slice: removing the only node destroys the cluster, so
// the caller confirms with the cluster name. The record is removed last so
// an interrupted uninstall re-detects as damaged rather than fresh.
func UninstallNode(ctx context.Context, runner host.Runner, record *Record, nodeCount int, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	if nodeCount > 1 {
		return errors.New("removing a node from a multi-node cluster is not implemented in this slice")
	}

	progress.Start("Uninstall k3s")
	if err := uninstallK3s(ctx, runner, record.Node.Role); err != nil {
		return err
	}
	progress.Done("")

	progress.Start("Remove " + StateDir)
	// Everything under StateDir goes except the record, which goes last.
	entries := []string{CacheDir, LogDir}
	for _, entry := range entries {
		if err := runner.Remove(ctx, entry); err != nil {
			return fmt.Errorf("remove %s: %w", entry, err)
		}
	}
	if err := RemoveRecord(ctx, runner); err != nil {
		return fmt.Errorf("remove installation record: %w", err)
	}
	if err := runner.Remove(ctx, StateDir); err != nil {
		return fmt.Errorf("remove %s: %w", StateDir, err)
	}
	progress.Done("")
	return nil
}

// DescribeBundleRemoval renders what a bundle uninstall destroys, for the
// confirmation prompt.
func DescribeBundleRemoval(inventory *BundleInventory) string {
	var builder strings.Builder
	builder.WriteString("Removing the Skali bundle destroys, permanently:\n")
	if len(inventory.ProjectNamespaces) == 0 {
		builder.WriteString("  - no project namespaces exist\n")
	} else {
		builder.WriteString("  - project namespaces: " + strings.Join(inventory.ProjectNamespaces, ", ") + "\n")
	}
	builder.WriteString("  - the skali-system namespace: control-plane state, databases, and all registry contents\n")
	builder.WriteString("  - the blessed operators: " + strings.Join(bundle.OperatorNamespaces, ", ") + "\n")
	builder.WriteString("Bare k3s keeps running.")
	return builder.String()
}
