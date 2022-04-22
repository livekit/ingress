package media

import (
	"sort"

	"github.com/livekit/protocol/livekit"
)

type StreamKind string

const (
	Audio StreamKind = "audio"
	Video StreamKind = "video"
)

// OutputStream holds one or more encoders for either an audio or video stream
type OutputStream struct {
	kind         StreamKind
	encoders     []*Encoder
	audioOptions *livekit.IngressAudioOptions
	videoOptions *livekit.IngressVideoOptions
}

func NewAudioOutputStream(options *livekit.IngressAudioOptions) (*OutputStream, error) {
	encoder, err := NewAudioEncoder(options)
	if err != nil {
		return nil, err
	}
	return &OutputStream{
		kind:         Audio,
		encoders:     []*Encoder{encoder},
		audioOptions: options,
	}, nil
}

func NewVideoOutputStream(options *livekit.IngressVideoOptions, inputLayer *livekit.VideoLayer) (*OutputStream, error) {
	s := &OutputStream{
		kind:         Video,
		videoOptions: options,
	}
	layers, err := computeSimulcastLayers(inputLayer, options.Layers)
	if err != nil {
		return nil, err
	}

	for _, l := range layers {
		encoder, err := NewVideoEncoder(options.MimeType, l)
		if err != nil {
			return nil, err
		}
		s.encoders = append(s.encoders, encoder)
	}
	return s, nil
}

func computeSimulcastLayers(inputLayer *livekit.VideoLayer, outputLayers []*livekit.VideoLayer) ([]*livekit.VideoLayer, error) {
	if inputLayer.Width == 0 || inputLayer.Height == 0 {
		return nil, ErrInvalidInputDimensions
	}
	if inputLayer.Fps == 0 {
		return nil, ErrInvalidInputFPS
	}

	var publishWidth, publishHeight uint32
	// when they are not explicitly given, we'll fill in up to 3 layers
	if len(outputLayers) == 0 {
		publishWidth = inputLayer.Width
		publishHeight = inputLayer.Height
		minDim := inputLayer.Width
		if inputLayer.Height < minDim {
			minDim = inputLayer.Height
		}
		numLayers := 3
		if minDim < 360 {
			numLayers = 1
		} else if minDim < 720 {
			numLayers = 2
		}

		for i := 0; i < numLayers; i++ {
			outputLayers = append(outputLayers, &livekit.VideoLayer{})
		}
	} else {
		for _, l := range outputLayers {
			if l.Width == 0 || l.Height == 0 {
				return nil, ErrInvalidOutputDimensions
			}
		}
		// sort by resolution (high to low) and only keep first 3 layers
		sort.Slice(outputLayers, func(i, j int) bool {
			return (outputLayers[i].Width + outputLayers[i].Height) > (outputLayers[j].Width + outputLayers[j].Height)
		})
		if len(outputLayers) > 3 {
			outputLayers = outputLayers[:3]
		}
		publishWidth = outputLayers[0].Width
		publishHeight = outputLayers[0].Height
	}

	scale := uint32(1) // drops by 1/2 each time
	// designed for 30/60 fps source: drops to 2/3, 1/3
	fpsScales := []float32{
		1,
		1.5,
		3,
	}

	numLayers := len(outputLayers)
	for i, layer := range outputLayers {
		layer.Quality = livekit.VideoQuality(numLayers - 1 - i)
		if layer.Width == 0 || layer.Height == 0 {
			layer.Width = publishWidth / scale
			layer.Height = publishHeight / scale
		}
		if layer.Fps == 0 {
			layer.Fps = inputLayer.Fps / fpsScales[i]
		}
		scale = scale * 2
	}
	return outputLayers, nil
}
