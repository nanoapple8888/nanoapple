package relay

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInjectImageResponseFormat(t *testing.T) {
	t.Run("injects when missing", func(t *testing.T) {
		out, err := injectImageResponseFormat([]byte(`{"model":"gpt-image-2","prompt":"cat"}`), "b64_json")
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"gpt-image-2","prompt":"cat","response_format":"b64_json"}`, string(out))
	})

	t.Run("preserves explicit value", func(t *testing.T) {
		in := []byte(`{"model":"gpt-image-2","prompt":"cat","response_format":"url"}`)
		out, err := injectImageResponseFormat(in, "b64_json")
		require.NoError(t, err)
		require.Equal(t, string(in), string(out))
	})

	t.Run("no-op for empty format", func(t *testing.T) {
		in := []byte(`{"model":"gpt-image-2","prompt":"cat"}`)
		out, err := injectImageResponseFormat(in, "")
		require.NoError(t, err)
		require.Equal(t, string(in), string(out))
	})
}
