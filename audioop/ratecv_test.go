package audioop

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRatecvShortBufferStereo(t *testing.T) {
	input := []byte{0x00, 0x00, 0x00, 0x00}

	var (
		out   []byte
		state *State
		err   error
	)

	assert.NotPanics(t, func() {
		out, state, err = Ratecv(input, 2, 2, 16000, 24000, 1, 0)
	})
	assert.NoError(t, err)
	assert.NotNil(t, state)
	assert.Equal(t, -2, state.D)
	assert.Greater(t, len(out), 0)
	assert.Equal(t, 0, len(out)%4)
}
