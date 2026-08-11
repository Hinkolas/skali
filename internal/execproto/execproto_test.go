package execproto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeDataCopies(t *testing.T) {
	buf := []byte("hello")
	frame := EncodeData(ChannelStdin, buf)
	require.Equal(t, append([]byte{ChannelStdin}, []byte("hello")...), frame)

	buf[0] = 'X'
	require.Equal(t, byte('h'), frame[1], "frame must not alias the caller's buffer")
}

func TestDataRoundtrip(t *testing.T) {
	frame := EncodeData(ChannelStdout, []byte("output"))
	channel, payload, err := Decode(frame)
	require.NoError(t, err)
	require.Equal(t, ChannelStdout, channel)
	require.Equal(t, []byte("output"), payload)
}

func TestDecodeEmptyPayload(t *testing.T) {
	channel, payload, err := Decode([]byte{ChannelStderr})
	require.NoError(t, err)
	require.Equal(t, ChannelStderr, channel)
	require.Empty(t, payload)
}

func TestDecodeEmptyFrame(t *testing.T) {
	_, _, err := Decode(nil)
	require.Error(t, err)
	_, _, err = Decode([]byte{})
	require.Error(t, err)
}

func TestControlRoundtrip(t *testing.T) {
	for _, control := range []Control{
		{Type: ControlResize, Cols: 120, Rows: 40},
		{Type: ControlStdinEOF},
		{Type: ControlExit, Code: 7},
		{Type: ControlExit, Code: 0},
		{Type: ControlError, ErrCode: ErrCodeExecFailed, Message: "container not found"},
	} {
		frame, err := EncodeControl(control)
		require.NoError(t, err)

		channel, payload, err := Decode(frame)
		require.NoError(t, err)
		require.Equal(t, ChannelControl, channel)

		parsed, err := ParseControl(payload)
		require.NoError(t, err)
		require.Equal(t, control, parsed)
	}
}

func TestParseControlUnknownType(t *testing.T) {
	// A newer peer may send control types this side does not know; they
	// must parse cleanly so the reader can skip them.
	parsed, err := ParseControl([]byte(`{"type":"detach","token":"x"}`))
	require.NoError(t, err)
	require.Equal(t, "detach", parsed.Type)
}

func TestParseControlInvalid(t *testing.T) {
	_, err := ParseControl([]byte("not json"))
	require.Error(t, err)
}
