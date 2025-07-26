package audio

import (
	"fmt"

	"layeh.com/gopus"
)

const (
	SampleRate = 48000
	Channels   = 1   // Mono
	FrameSize  = 960 // 20ms at 48kHz
)

type OpusDecoder struct {
	decoder *gopus.Decoder
}

func NewOpusDecoder() (*OpusDecoder, error) {
	decoder, err := gopus.NewDecoder(SampleRate, Channels)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus decoder: %w", err)
	}

	return &OpusDecoder{
		decoder: decoder,
	}, nil
}

func (d *OpusDecoder) Decode(opus []byte) ([]int16, error) {
	// Handle silence frames
	if len(opus) == 3 && opus[0] == 0xF8 && opus[1] == 0xFF && opus[2] == 0xFE {
		// Return silence for comfort noise frames
		return make([]int16, FrameSize), nil
	}

	pcm, err := d.decoder.Decode(opus, FrameSize, false)
	if err != nil {
		return nil, fmt.Errorf("failed to decode opus: %w", err)
	}

	return pcm, nil
}

func (d *OpusDecoder) Close() {
	// gopus decoder doesn't require explicit cleanup
}
