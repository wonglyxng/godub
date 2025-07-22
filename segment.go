package godub

import (
	"bytes"
	"math"

	"fmt"

	"encoding/binary"

	"github.com/wonglyxng/godub/audioop"
	"github.com/wonglyxng/godub/utils"
	"github.com/wonglyxng/godub/wav"
)

var (
	ValidChannels = utils.NewSet(1, 2)
)

// AudioSegment represents an segment of audio that can be
// manipulated using Go code.
// AudioSegment is **immutable**.
type AudioSegment struct {
	sampleWidth uint16
	frameRate   uint32
	frameWidth  uint32
	channels    uint16
	data        []byte

	// Cached values, because audio segment is immutable
	// it's safe to store it.
	rms *float64
}

func (seg *AudioSegment) String() string {
	return fmt.Sprintf(
		"AudioSegment(sample_width=%d, frame_rate=%d, frame_width=%d, channels=%d, duration=%dms)",
		seg.sampleWidth, seg.frameRate, seg.frameWidth, seg.channels, seg.Duration(),
	)
}

// Factory functions
func NewAudioSegment(data []byte, opts ...AudioSegmentOption) (*AudioSegment, error) {
	seg := &AudioSegment{data: data}
	for _, opt := range opts {
		opt(seg)
	}

	// FIXME: check if sample params are all valid or not.
	// Convert 24-bit audio to 32-bit audio. Package audioop only supports 32-bit data.
	if seg.sampleWidth == 3 {
		bytesLen := len(data)/int(seg.sampleWidth) + len(data)
		buf := make([]byte, bytesLen)

		offset := 0
		for i := 0; i < len(data); i += 3 {
			b0, b1, b2 := data[i], data[i+1], data[i+2]

			var padding byte
			if b2 > 127 {
				padding = 0xFF
			}

			w := bytes.NewBuffer(buf[offset:])
			binary.Write(w, binary.LittleEndian, []byte{padding, b0, b1, b2})

			// Next available position to write
			offset += 4
		}

		seg.data = buf
		seg.sampleWidth = 4
	}
	return seg, nil
}

func NewEmptyAudioSegment() (*AudioSegment, error) {
	return NewAudioSegment(
		[]byte{},
		Channels(1),
		SampleWidth(1),
		FrameRate(1),
		FrameWidth(1),
	)
}

func NewSilentAudioSegment(duration int, frameRate uint32) (*AudioSegment, error) {
	frames := int(float64(frameRate) * (float64(duration) / 1000.0))
	data := bytes.Repeat([]byte("\x00\x00"), frames)
	return NewAudioSegment(
		data,
		Channels(1),
		SampleWidth(2),
		FrameWidth(2),
		FrameRate(frameRate),
	)
}

func NewAudioSegmentFromWaveAudio(waveAudio *wav.WaveAudio) (*AudioSegment, error) {
	sampleWidth := waveAudio.BitsPerSample / 8
	return NewAudioSegment(
		waveAudio.RawData,
		Channels(waveAudio.Channels),
		SampleWidth(sampleWidth),
		FrameRate(waveAudio.SampleRate),
		FrameWidth(uint32(waveAudio.Channels*sampleWidth)),
	)
}

func (seg *AudioSegment) AsWaveAudio() *wav.WaveAudio {
	waveAudio := wav.WaveAudio{
		Format:        wav.AudioFormatPCM,
		Channels:      seg.channels,
		RawData:       seg.data,
		BitsPerSample: seg.sampleWidth * 8,
		SampleRate:    seg.frameRate,
	}
	return &waveAudio
}

// Operations

// Slice 从音频片段中截取指定时间范围的部分
//
// 参数:
//   - start: 起始时间(毫秒)
//   - end: 结束时间(毫秒)
//
// 返回:
//   - *AudioSegment: 截取的新音频片段
//   - error: 错误信息,如果有的话
//
// 注意事项:
//   - start必须小于end
//   - start和end必须为非负数
//   - 如果end超过音频长度,将截取到音频末尾
//   - 对于缺失的帧会用静音填充(最多2ms)
func (seg *AudioSegment) Slice(start, end int64) (*AudioSegment, error) {
	if start > end {
		return nil, NewAudioSegmentError("start should be smaller than end")
	}

	if start < 0 || end < 0 {
		return nil, NewAudioSegmentError("start or end should be positive")
	}

	audioLength := seg.Duration()
	if start > audioLength {
		start = audioLength
	}

	if end > audioLength {
		end = audioLength
	}

	startIndex := seg.parsePosition(start) * int(seg.frameWidth)
	endIndex := seg.parsePosition(end) * int(seg.frameWidth)
	expectedLength := endIndex - startIndex

	if endIndex > len(seg.data) {
		endIndex = len(seg.data)
	}

	data := seg.data[startIndex:endIndex]

	// Ensure the output is as long as the user is expecting
	missingFrames := (expectedLength - len(data)) / int(seg.frameWidth)
	if missingFrames > 0 {
		if float64(missingFrames) > seg.FrameCountIn(2) {
			return nil, NewAudioSegmentError(
				"you should never be filling in more than 2ms with silence here, missing %d frames",
				missingFrames)
		}

		if silence, err := audioop.Mul(data[:seg.frameWidth], int(seg.sampleWidth), 0); err != nil {
			silences := bytes.Repeat(silence, missingFrames)
			data = utils.ConcatenateByteSlice(data, silences)
		}
	}

	return seg.derive(data)
}

func (seg *AudioSegment) Add(other *AudioSegment) (*AudioSegment, error) {
	return seg.Append(other)
}

func (seg *AudioSegment) Append(segments ...*AudioSegment) (*AudioSegment, error) {
	combined := []*AudioSegment{seg}
	combined = append(combined, segments...)

	results, err := sync(combined...)
	if err != nil {
		return nil, err
	}

	data := make([][]byte, 0)
	for _, r := range results {
		data = append(data, r.data)
	}
	return seg.derive(utils.ConcatenateByteSlice(data...))
}

func (seg *AudioSegment) Equal(other *AudioSegment) bool {
	return bytes.Equal(seg.data, other.data)
}

func (seg *AudioSegment) ApplyGain(volumeChange Volume) (*AudioSegment, error) {
	data, err := audioop.Mul(seg.data, int(seg.sampleWidth), volumeChange.ToRatio(true))
	if err != nil {
		return nil, err
	}
	return seg.derive(data)
}

func (seg *AudioSegment) Repeat(count int) (*AudioSegment, error) {
	return seg.derive(bytes.Repeat(seg.data, count))
}

func (seg *AudioSegment) Reverse() (*AudioSegment, error) {
	data, err := audioop.Reverse(seg.data, int(seg.sampleWidth))
	if err != nil {
		return nil, err
	}
	return seg.derive(data)
}

func (seg *AudioSegment) ForkWithSampleWidth(sampleWidth int) (*AudioSegment, error) {
	if sampleWidth == int(seg.sampleWidth) {
		return seg, nil
	}

	data := seg.data

	if seg.sampleWidth == 1 {
		if ret, err := audioop.Bias(data, 1, -128); err != nil {
			return nil, err
		} else {
			data = ret
		}
	}

	if len(data) > 0 {
		if ret, err := audioop.Lin2Lin(data, int(seg.sampleWidth), sampleWidth); err != nil {
			return nil, err
		} else {
			data = ret
		}
	}

	if sampleWidth == 1 {
		if ret, err := audioop.Bias(data, 1, 128); err != nil {
			return nil, err
		} else {
			data = ret
		}
	}

	frameWidth := int(seg.channels) * sampleWidth
	return seg.derive(data, SampleWidth(uint16(sampleWidth)), FrameWidth(uint32(frameWidth)))
}

func (seg *AudioSegment) ForkWithFrameRate(frameRate int) (*AudioSegment, error) {
	if frameRate == int(seg.frameRate) {
		return seg, nil
	}

	converted := seg.data
	if len(seg.data) > 0 {
		ret, _, err := audioop.Ratecv(
			seg.data,
			int(seg.sampleWidth),
			int(seg.channels),
			int(seg.frameRate),
			frameRate,
			1,
			0,
		)
		if err != nil {
			return nil, err
		}
		converted = ret
	}

	return seg.derive(converted, FrameRate(uint32(frameRate)))
}

func (seg *AudioSegment) ForkWithChannels(channels uint16) (*AudioSegment, error) {
	if !ValidChannels.Has(int(channels)) {
		return nil, NewAudioSegmentError("invalid channels")
	}

	// Feature: copy-on-write
	if channels == seg.channels {
		return seg, nil
	}

	var convertFunc func(cp []byte, size int, fac1, fac2 float64) ([]byte, error)
	var frameWidth int
	var fac float64

	if channels == 2 && seg.channels == 1 {
		convertFunc = audioop.ToStereo
		frameWidth = int(seg.frameWidth) * 2
		fac = 1
	} else if channels == 1 && seg.channels == 2 {
		convertFunc = audioop.ToMono
		frameWidth = int(seg.frameWidth) / 2
		fac = 0.5
	}

	converted, err := convertFunc(seg.data, int(seg.sampleWidth), fac, fac)
	if err != nil {
		return nil, err
	}

	return seg.derive(converted, Channels(channels), FrameWidth(uint32(frameWidth)))
}

type OverlayConfig struct {
	// Position to start overlaying, milliseconds
	Position int64
	// LoopToEnd indicates whether it's necessary to match the original segment's length
	LoopToEnd bool
	// LoopCount indicates that we should loop the segment for `LoopCount` times
	// until it matches the original segment length, default to 1.
	LoopCount         int
	GainDuringOverlay Volume
}

// Overlay overlays the given audio segment on the current segment.
// Overlay 在当前音频片段上叠加另一个音频片段
//
// 参数:
//   - other: 要叠加的音频片段
//   - config: 叠加配置,包含:
//   - Position: 开始叠加的位置(毫秒)
//   - LoopToEnd: 是否循环叠加直到原始音频结束
//   - LoopCount: 循环次数(LoopToEnd为true时忽略)
//   - GainDuringOverlay: 叠加时的音量增益
//
// 注意:
//   - 如果other为nil,返回原始音频段
//   - 叠加前会先同步两个音频段的采样参数
//   - LoopCount默认为1,当LoopToEnd为true时设为-1表示无限循环
func (seg *AudioSegment) Overlay(other *AudioSegment, config *OverlayConfig) (*AudioSegment, error) {
	if other == nil {
		return seg.derive(seg.data)
	}

	if config.LoopCount == 0 {
		config.LoopCount = 1
	}

	if config.LoopToEnd {
		// Set to -1, so that we can loop until the end.
		config.LoopCount = -1
	}

	syncedSegments, err := sync(seg, other)
	if err != nil {
		return nil, err
	}
	segment, other := syncedSegments[0], syncedSegments[1]

	// Dest buffer to save overlaid data.
	var dest []byte
	destBuf := bytes.NewBuffer(dest)

	// Cut the left part, and store it to the buffer.
	if r, err := segment.Slice(0, config.Position); err != nil {
		return nil, err
	} else {
		_, err := destBuf.Write(r.data)
		if err != nil {
			return nil, err
		}
	}

	rSegment, err := segment.Slice(config.Position, segment.Duration())
	if err != nil {
		return nil, err
	}

	sampleWidth := int(segment.sampleWidth)
	rSegLen := len(rSegment.data)
	rSegData := rSegment.data

	otherSegLen := len(other.data)
	otherSegData := other.data

	pos := 0
	for i := config.LoopCount; i != 0; i -= 1 {
		remainingLen := rSegLen - pos
		if remainingLen < 0 {
			remainingLen = 0
		}

		if otherSegLen >= remainingLen {
			otherSegData = otherSegData[:remainingLen]
			otherSegLen = remainingLen
			// Mark this is the last round.
			i = 1
		}

		var overlaidBytes []byte
		if config.GainDuringOverlay > 0 {
			adjustedBytes, err := audioop.Mul(
				rSegData[pos:pos+otherSegLen],
				sampleWidth,
				config.GainDuringOverlay.ToRatio(true),
			)
			if err != nil {
				return nil, err
			}

			r, err := audioop.Add(adjustedBytes, otherSegData, sampleWidth)
			if err != nil {
				return nil, err
			}

			overlaidBytes = r
		} else {
			r, err := audioop.Add(rSegData[pos:pos+otherSegLen], otherSegData, sampleWidth)
			if err != nil {
				return nil, err
			}

			overlaidBytes = r
		}

		_, err := destBuf.Write(overlaidBytes)
		if err != nil {
			return nil, err
		}

		// Move to the next position
		pos += otherSegLen
	}

	_, err = destBuf.Write(rSegData[pos:])
	if err != nil {
		return nil, err
	}

	return segment.derive(destBuf.Bytes())
}

// RMS returns the value of root mean square
// RMS 返回音频片段的均方根值(Root Mean Square)
//
// 计算过程:
//  1. 如果已经缓存了RMS值,直接返回
//  2. 对于1字节采样宽度的音频,先转换为2字节后再计算
//  3. 使用audioop.RMS计算均方根值
//
// 注意:
//   - 如果计算过程中发生错误,将返回0
func (seg *AudioSegment) RMS() float64 {
	if seg.rms != nil {
		return *seg.rms
	}

	if seg.sampleWidth == 1 {
		if r, err := seg.ForkWithSampleWidth(2); err != nil {
			return 0
		} else {
			return r.RMS()
		}
	} else {
		r, err := audioop.RMS(seg.data, int(seg.sampleWidth))
		if err != nil {
			return 0
		}
		rms := float64(r)
		seg.rms = &rms
		return rms
	}
}

// DBFS returns the value of dB Full Scale
// DBFS 返回音频片段的dB全幅度值(dB Full Scale)
//
// 计算方式:
//
//	DBFS = 20 * log10(RMS值 / 最大振幅)
//
// 说明:
//   - DBFS表示相对于最大可能振幅的分贝值
//   - 值始终为负数或0,0表示最大振幅
//   - 值越小表示音量越小
func (seg *AudioSegment) DBFS() Volume {
	return NewVolumeFromRatio(seg.RMS()/seg.MaxPossibleAmplitude(), 0, true)
}

// MaxPossibleAmplitude 返回音频片段可能的最大振幅值
//
// 计算方式:
//
//	最大振幅 = (2^采样位数) / 2
//
// 说明:
//   - 对于16位音频,最大振幅为32768(2^16/2)
//   - 对于8位音频,最大振幅为128(2^8/2)
//   - 振幅范围在[-最大振幅,+最大振幅]之间
func (seg *AudioSegment) MaxPossibleAmplitude() float64 {
	bits := seg.sampleWidth * 8
	maxPossibleVal := math.Pow(2, float64(bits))
	// Since half is above 0 and half is below the max amplitude is divided
	return maxPossibleVal / 2
}

// MaxDBFS 返回音频片段最大振幅的dB全幅度值
//
// 计算方式:
//
//	MaxDBFS = 20 * log10(最大振幅值 / 最大可能振幅)
//
// 说明:
//   - 与DBFS类似,但使用最大振幅而非RMS值计算
//   - 值始终为负数或0,0表示达到最大可能振幅
func (seg *AudioSegment) MaxDBFS() Volume {
	return NewVolumeFromRatio(seg.Max(), seg.MaxPossibleAmplitude(), true)
}

// Max 返回音频片段中的最大振幅值
//
// 说明:
//   - 使用audioop.Max计算原始数据中的最大值
//   - 如果计算出错则返回0
func (seg *AudioSegment) Max() float64 {
	if r, err := audioop.Max(seg.data, int(seg.sampleWidth)); err != nil {
		return 0
	} else {
		return float64(r)
	}
}

// Duration 返回音频片段的时长(毫秒)
//
// 计算方式:
//
//	duration = (帧数 / 帧率) * 1000
//
// 注意:
//   - 如果帧率为0,将返回0
func (seg *AudioSegment) Duration() int64 {
	if seg.frameRate == 0 {
		return 0
	}
	mills := math.Round(1000.0 * (seg.FrameCount() / float64(seg.frameRate)))
	return int64(mills)
}

// FrameCount 返回音频片段的总帧数
//
// 计算方式:
//
//	帧数 = 数据长度 / 每帧字节数
//
// 注意:
//   - 如果frameWidth为0,将返回0
func (seg *AudioSegment) FrameCount() float64 {
	if seg.frameWidth > 0 {
		return float64(len(seg.data) / int(seg.frameWidth))
	} else {
		return 0
	}
}

// FrameCountIn 计算指定毫秒数对应的帧数
//
// 参数:
//   - ms: 毫秒数
//
// 返回:
//   - float64: 对应的帧数
//
// 注意:
//   - 如果传入的毫秒数大于音频总时长,将使用音频总时长计算
//   - 计算方式: 帧数 = 毫秒数 * (帧率 / 1000)
func (seg *AudioSegment) FrameCountIn(ms int64) float64 {
	duration := seg.Duration()
	if ms > duration {
		ms = duration
	}

	//ms := utils.Milliseconds(d)
	return float64(ms) * (float64(seg.frameRate) / 1000.0)
}

func (seg *AudioSegment) SampleWidth() uint16 {
	return seg.sampleWidth
}

func (seg *AudioSegment) FrameRate() uint32 {
	return seg.frameRate
}

func (seg *AudioSegment) FrameWidth() uint32 {
	return seg.frameWidth
}

func (seg *AudioSegment) Channels() uint16 {
	return seg.channels
}

func (seg *AudioSegment) RawData() []byte {
	return seg.data
}

// Private functions & methods
// sync will make sure every input segments have identical channels, frame rate and sample width.
// sync 确保所有输入的音频片段具有相同的声道数、采样率和采样宽度
//
// 参数:
//   - segments: 需要同步的音频片段列表
//
// 返回:
//   - []*AudioSegment: 同步后的音频片段列表
//   - error: 同步过程中的错误信息
//
// 同步过程:
//  1. 收集所有片段的声道数、采样率和采样宽度
//  2. 选择最大值作为目标参数
//  3. 依次对每个片段进行转换:
//     - 调整声道数(mono/stereo)
//     - 调整采样率(up/down sampling)
//     - 调整采样宽度(8/16/24/32 bit)
//
// 注意:
//   - 同步会创建新的音频片段,不会修改原始片段
//   - 转换过程可能会降低音频质量
func sync(segments ...*AudioSegment) ([]*AudioSegment, error) {
	allChannels := make([]uint16, 0)
	allFrameRates := make([]uint32, 0)
	allSampleWidths := make([]uint16, 0)

	for _, seg := range segments {
		allChannels = append(allChannels, seg.channels)
		allFrameRates = append(allFrameRates, seg.frameRate)
		allSampleWidths = append(allSampleWidths, seg.sampleWidth)
	}

	maxChannels := utils.MaxUint16(allChannels...)
	maxFrameRate := utils.MaxUint32(allFrameRates...)
	maxSampleWidth := utils.MaxUint16(allSampleWidths...)

	newSegments := make([]*AudioSegment, 0)
	for _, seg := range segments {
		newSeg := seg
		if r, err := seg.ForkWithChannels(maxChannels); err != nil {
			return nil, err
		} else {
			newSeg = r
		}

		if r, err := newSeg.ForkWithFrameRate(int(maxFrameRate)); err != nil {
			return nil, err
		} else {
			newSeg = r
		}

		if r, err := newSeg.ForkWithSampleWidth(int(maxSampleWidth)); err != nil {
			return nil, err
		} else {
			newSeg = r
		}

		newSegments = append(newSegments, newSeg)
	}

	return newSegments, nil
}

// derive creates a new audio segment with config from the current one.
// derive 基于当前音频段创建新的音频段
//
// 参数:
//   - data: 新的音频数据
//   - opts: 可选的音频段参数
//
// 说明:
//   - 继承当前音频段的所有参数(采样宽度、帧率等)
//   - 可通过opts覆盖继承的参数
//   - 实现了音频段的不可变特性
func (seg *AudioSegment) derive(data []byte, opts ...AudioSegmentOption) (*AudioSegment, error) {
	ret, err := NewAudioSegment(
		data,
		SampleWidth(seg.sampleWidth),
		FrameRate(seg.frameRate),
		FrameWidth(seg.frameWidth),
		Channels(seg.channels),
	)
	if err != nil {
		return nil, err
	}

	for _, opt := range opts {
		opt(ret)
	}

	return ret, nil
}

func (seg *AudioSegment) parsePosition(val int64) int {
	frames := seg.FrameCountIn(val)
	return int(frames)
}
