package media

import (
	"context"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	info := &livekit.IngressInfo{}

	err := Validate(context.Background(), info)
	require.Error(t, err)

	info.StreamKey = "stream_key"
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.RoomName = "room_name"
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.ParticipantIdentity = "participant_identity"
	err = Validate(context.Background(), info)
	require.NoError(t, err)

	// Video
	info.Video = &livekit.IngressVideoOptions{}
	err = Validate(context.Background(), info)
	require.NoError(t, err)

	info.Video.Source = livekit.TrackSource_MICROPHONE
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.Video.Source = livekit.TrackSource_CAMERA
	info.Video.EncodingOptions = &livekit.IngressVideoOptions_Preset{
		Preset: livekit.IngressVideoEncodingPreset(42),
	}
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.Video.EncodingOptions = &livekit.IngressVideoOptions_Preset{
		Preset: livekit.IngressVideoEncodingPreset_H264_1080P_30FPS_1_LAYER,
	}
	err = Validate(context.Background(), info)
	require.NoError(t, err)

	info.Video.EncodingOptions = &livekit.IngressVideoOptions_Options{
		Options: &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_HIGH,
		},
	}
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.Video.EncodingOptions = &livekit.IngressVideoOptions_Options{
		Options: &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_DEFAULT_VC,
		},
	}
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.Video.EncodingOptions.(*livekit.IngressVideoOptions_Options).Options.Layers = []*livekit.VideoLayer{
		&livekit.VideoLayer{},
	}
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.Video.EncodingOptions.(*livekit.IngressVideoOptions_Options).Options.Layers = []*livekit.VideoLayer{
		&livekit.VideoLayer{
			Width:  640,
			Height: 480,
		},
	}
	err = Validate(context.Background(), info)
	require.NoError(t, err)

	// Audio
	info.Audio = &livekit.IngressAudioOptions{}
	err = Validate(context.Background(), info)
	require.NoError(t, err)

	info.Audio.Source = livekit.TrackSource_CAMERA
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.Audio.Source = livekit.TrackSource_SCREEN_SHARE_AUDIO
	info.Audio.EncodingOptions = &livekit.IngressAudioOptions_Preset{
		Preset: livekit.IngressAudioEncodingPreset(42),
	}
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.Audio.EncodingOptions = &livekit.IngressAudioOptions_Preset{
		Preset: livekit.IngressAudioEncodingPreset_OPUS_MONO_64KBS,
	}
	err = Validate(context.Background(), info)
	require.NoError(t, err)

	info.Audio.EncodingOptions = &livekit.IngressAudioOptions_Options{
		Options: &livekit.IngressAudioEncodingOptions{
			AudioCodec: livekit.AudioCodec_AAC,
		},
	}
	err = Validate(context.Background(), info)
	require.Error(t, err)

	info.Audio.EncodingOptions = &livekit.IngressAudioOptions_Options{
		Options: &livekit.IngressAudioEncodingOptions{
			AudioCodec: livekit.AudioCodec_OPUS,
		},
	}
	err = Validate(context.Background(), info)
	require.NoError(t, err)

}

func TestPopulateAudioEncodingOptionsDefaults(t *testing.T) {
	in := &livekit.IngressAudioEncodingOptions{}

	out, err := populateAudioEncodingOptionsDefaults(in)
	require.NoError(t, err)
	require.Equal(t, livekit.AudioCodec_OPUS, out.AudioCodec)
	require.Equal(t, uint32(2), out.Channels)
	require.Equal(t, uint32(96000), out.Bitrate)

	in.Channels = 1
	out, err = populateAudioEncodingOptionsDefaults(in)
	require.NoError(t, err)
	require.Equal(t, livekit.AudioCodec_OPUS, out.AudioCodec)
	require.Equal(t, uint32(1), out.Channels)
	require.Equal(t, uint32(64000), out.Bitrate)

}

func TestPopulateVideoEncodingOptionsDefaults(t *testing.T) {
	in := &livekit.IngressVideoEncodingOptions{}

	out, err := populateVideoEncodingOptionsDefaults(in)
	require.NoError(t, err)
	require.Equal(t, livekit.VideoCodec_H264_BASELINE, out.VideoCodec)
	require.Equal(t, float64(30), out.FrameRate)
	require.Equal(t, expectedDefaultLayers, out.Layers)

	in.FrameRate = 15
	in.Layers = []*livekit.VideoLayer{
		&livekit.VideoLayer{
			Width:   1920,
			Height:  1080,
			Quality: livekit.VideoQuality_HIGH,
		},
		&livekit.VideoLayer{
			Width:   480,
			Height:  270,
			Quality: livekit.VideoQuality_LOW,
		},
	}
	expected := []*livekit.VideoLayer{
		&livekit.VideoLayer{
			Width:   1920,
			Height:  1080,
			Bitrate: 1_783_810,
			Quality: livekit.VideoQuality_HIGH,
		},
		&livekit.VideoLayer{
			Width:   480,
			Height:  270,
			Bitrate: 222_976,
			Quality: livekit.VideoQuality_LOW,
		},
	}

	out, err = populateVideoEncodingOptionsDefaults(in)
	require.NoError(t, err)
	require.Equal(t, livekit.VideoCodec_H264_BASELINE, out.VideoCodec)
	require.Equal(t, float64(15), out.FrameRate)
	require.Equal(t, expected, out.Layers)
}
