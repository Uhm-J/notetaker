package audio

import (
	"math"

	"github.com/maxhawkins/go-webrtcvad"
)

type WebRTCVAD struct {
	vad         *webrtcvad.VAD
	rmsThreshold float64
}

func NewWebRTCVAD() (*WebRTCVAD, error) {
	vad, err := webrtcvad.New()
	if err != nil {
		return nil, err
	}

	// Set aggressiveness (0-3, where 3 is most aggressive)
	vad.SetMode(2)

	return &WebRTCVAD{
		vad:         vad,
		rmsThreshold: 500.0, // Fallback RMS threshold
	}, nil
}

func (v *WebRTCVAD) IsSpeech(pcm []int16, sampleRate int) bool {
	// Convert to byte slice for WebRTC VAD
	bytes := int16SliceToBytes(pcm)
	
	// WebRTC VAD expects specific frame sizes
	if len(bytes) < 320 { // 10ms at 16kHz = 320 bytes
		return v.rmsIsSpeech(pcm)
	}

	// Try WebRTC VAD first
	isSpeech, err := v.vad.Process(sampleRate, bytes)
	if err != nil {
		return v.rmsIsSpeech(pcm)
	}
	return isSpeech
}

func (v *WebRTCVAD) rmsIsSpeech(pcm []int16) bool {
	if len(pcm) == 0 {
		return false
	}

	var sum float64
	for _, sample := range pcm {
		sum += float64(sample) * float64(sample)
	}
	
	rms := math.Sqrt(sum / float64(len(pcm)))
	return rms > v.rmsThreshold
}

func (v *WebRTCVAD) Close() error {
	if v.vad != nil {
		v.vad.Close()
	}
	return nil
}

func int16SliceToBytes(samples []int16) []byte {
	bytes := make([]byte, len(samples)*2)
	for i, sample := range samples {
		bytes[i*2] = byte(sample)
		bytes[i*2+1] = byte(sample >> 8)
	}
	return bytes
}