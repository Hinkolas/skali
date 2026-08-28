package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

func TestVerificationURL(t *testing.T) {
	require.Equal(t, "https://skali.example.com/auth/device?code=BCDF-2345",
		verificationURL("https://skali.example.com/api", "BCDF-2345"))
	require.Equal(t, "https://skali.example.com/auth/device?code=BCDF-2345",
		verificationURL("https://skali.example.com/api/", "BCDF-2345"))
	require.Equal(t, "http://host:7070/auth/device?code=BCDF-2345",
		verificationURL("http://host:7070", "BCDF-2345"))
}

func fakeDeviceStart(t *testing.T) deviceStart {
	t.Helper()
	return func(context.Context) (*client.DeviceRequest, error) {
		return &client.DeviceRequest{DeviceCode: "secret", UserCode: "BCDF-2345", Interval: 0, ExpiresAt: time.Now().Add(time.Minute)}, nil
	}
}

func TestRunDeviceFlowApproved(t *testing.T) {
	opened := stubDeviceFlow(t)
	var out bytes.Buffer
	polls := 0
	poll := func(_ context.Context, code string) (*client.DevicePoll, error) {
		require.Equal(t, "secret", code)
		polls++
		switch polls {
		case 1:
			return &client.DevicePoll{Status: client.DevicePending}, nil
		case 2:
			return nil, &client.APIError{Status: 429, Code: "slow_down"}
		default:
			return &client.DevicePoll{Status: client.DeviceApproved, Session: &client.SessionCreated{Token: "tok"}}, nil
		}
	}
	res, err := runDeviceFlowWithInterval(t, &out, fakeDeviceStart(t), poll)
	require.NoError(t, err)
	require.Equal(t, "tok", res.Session.Token)
	require.Equal(t, 3, polls)
	require.Contains(t, out.String(), "https://skali.example.com/auth/device?code=BCDF-2345")
	require.Contains(t, out.String(), "Code: BCDF-2345")
	require.Equal(t, "https://skali.example.com/auth/device?code=BCDF-2345", *opened)
}

func TestRunDeviceFlowDeniedAndCancelled(t *testing.T) {
	stubDeviceFlow(t)
	var out bytes.Buffer
	_, err := runDeviceFlowWithInterval(t, &out, fakeDeviceStart(t), func(context.Context, string) (*client.DevicePoll, error) {
		return &client.DevicePoll{Status: client.DeviceDenied}, nil
	})
	require.EqualError(t, err, "the request was denied in the browser")

	_, err = runDeviceFlowWithInterval(t, &out, fakeDeviceStart(t), func(context.Context, string) (*client.DevicePoll, error) {
		return &client.DevicePoll{Status: client.DeviceExpired}, nil
	})
	require.ErrorContains(t, err, "expired")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runDeviceFlow(ctx, &out, "https://skali.example.com/api", fakeDeviceStart(t), func(context.Context, string) (*client.DevicePoll, error) {
		return nil, errors.New("should not poll")
	})
	require.ErrorContains(t, err, "cancelled")

	_, err = runDeviceFlow(context.Background(), &out, "https://skali.example.com/api", func(context.Context) (*client.DeviceRequest, error) {
		return nil, &client.APIError{Status: 404, Code: "not_found"}
	}, nil)
	require.True(t, client.IsNotFound(err))
}

// stubDeviceFlow makes the loop fast and keeps the opener from launching
// anything; it returns where the opened URL lands.
func stubDeviceFlow(t *testing.T) *string {
	t.Helper()
	var opened string
	prevMin, prevOpen := devicePollMinimum, openBrowser
	devicePollMinimum = time.Millisecond
	openBrowser = func(url string) error { opened = url; return nil }
	t.Cleanup(func() { devicePollMinimum, openBrowser = prevMin, prevOpen })
	return &opened
}

func runDeviceFlowWithInterval(t *testing.T, out *bytes.Buffer, start deviceStart, poll devicePoll) (*client.DevicePoll, error) {
	t.Helper()
	return runDeviceFlow(context.Background(), out, "https://skali.example.com/api", start, poll)
}
