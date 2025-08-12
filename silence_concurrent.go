package godub

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/wonglyxng/godub/audioop"
)

// DetectSilenceConcurrent 是DetectSilence的并发优化版本
// 通过并行计算RMS值来提高大音频文件的静音检测性能
//
// 性能优化要点:
// 1. 使用多个goroutine并行处理音频片段
// 2. 直接在原始数据上计算RMS，避免创建AudioSegment对象
// 3. 减少内存分配和数据复制
func DetectSilenceConcurrent(seg *AudioSegment, minSilenceLen int64, silenceThresh Volume, seekStep int) [][]int64 {
	segLen := seg.Duration()

	// you can't have a silent portion of a sound that is longer than the sound
	if segLen < minSilenceLen {
		var emp [][]int64
		return emp
	}

	// convert silence threshold to a float value (so we can compare it to rms)
	var silThresh = silenceThresh.ToRatio(true) * seg.MaxPossibleAmplitude()

	// find silence and add start and end indicies to the to_cut list
	var silenceStarts []int64

	// check successive (1 ms by default) chunk of sound for silence
	// try a chunk at every "seek step" (or every chunk for a seek step == 1)
	lastSliceStart := segLen - minSilenceLen

	var sliceStarts []int64
	for i := int64(0); i < lastSliceStart+1; i += int64(seekStep) {
		sliceStarts = append(sliceStarts, i)
	}

	// guarantee lastSliceStart is included in the range
	// to make sure the last portion of the audio is searched
	if (lastSliceStart % int64(seekStep)) != 0 {
		sliceStarts = append(sliceStarts, lastSliceStart)
	}

	// 并发处理音频片段
	numWorkers := runtime.NumCPU()
	if numWorkers > len(sliceStarts) {
		numWorkers = len(sliceStarts)
	}

	// 结果通道，存储静音片段的起始位置
	type silenceResult struct {
		index    int
		position int64
		isSilent bool
	}

	resultCh := make(chan silenceResult, len(sliceStarts))
	var wg sync.WaitGroup

	// 工作函数
	worker := func(positions []int64, startIdx int) {
		defer wg.Done()

		for i, pos := range positions {
			// 直接计算RMS，避免创建新的AudioSegment
			rms := calculateRMSForSegmentOptimized(seg, pos, pos+minSilenceLen)

			resultCh <- silenceResult{
				index:    startIdx + i,
				position: pos,
				isSilent: rms <= silThresh,
			}
		}
	}

	// 分配工作
	chunkSize := (len(sliceStarts) + numWorkers - 1) / numWorkers
	for i := 0; i < numWorkers; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(sliceStarts) {
			end = len(sliceStarts)
		}
		if start < end {
			wg.Add(1)
			go worker(sliceStarts[start:end], start)
		}
	}

	// 等待所有工作完成
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// 收集结果
	results := make([]silenceResult, len(sliceStarts))
	for result := range resultCh {
		results[result.index] = result
	}

	// 按顺序提取静音位置
	for _, result := range results {
		if result.isSilent {
			silenceStarts = append(silenceStarts, result.position)
		}
	}

	// short circuit when there is no silence
	if len(silenceStarts) == 0 {
		var silentRanges [][]int64
		return silentRanges
	}

	// combine the silence we detected into ranges (start ms - end ms)
	var silentRanges [][]int64

	prevI, silenceStarts := silenceStarts[0], silenceStarts[1:]
	currentRangeStart := prevI

	for _, silenceStartI := range silenceStarts {
		var continuous bool
		var silenceHasGap bool
		if silenceStartI == prevI+int64(seekStep) {
			continuous = true
		} else {
			continuous = false
		}

		// sometimes two small blips are enough for one particular slice to be
		// non-silent, despite the silence all running together. Just combine
		// the two overlapping silent ranges.

		if silenceStartI > prevI+minSilenceLen {
			silenceHasGap = true
		} else {
			silenceHasGap = false
		}

		if !continuous && silenceHasGap {
			silentRanges = append(silentRanges, []int64{currentRangeStart, prevI + minSilenceLen})
			currentRangeStart = silenceStartI
		}
		prevI = silenceStartI
	}
	silentRanges = append(silentRanges, []int64{currentRangeStart, prevI + minSilenceLen})

	return silentRanges
}

// calculateRMSForSegmentOptimized 直接计算音频片段的RMS，避免创建新的AudioSegment
// 这是性能优化的核心：直接在原始数据上操作，避免内存分配
func calculateRMSForSegmentOptimized(seg *AudioSegment, start, end int64) float64 {
	// 将时间转换为字节索引
	startIndex := seg.parsePosition(start) * int(seg.frameWidth)
	endIndex := seg.parsePosition(end) * int(seg.frameWidth)

	if endIndex > len(seg.data) {
		endIndex = len(seg.data)
	}

	if startIndex >= endIndex {
		return 0
	}

	// 直接在数据切片上计算RMS
	dataSlice := seg.data[startIndex:endIndex]
	rms, err := audioop.RMS(dataSlice, int(seg.sampleWidth))
	if err != nil {
		return 0
	}

	return float64(rms)
}

// DetectNonsilentConcurrent 是DetectNonsilent的并发优化版本
func DetectNonsilentConcurrent(seg *AudioSegment, minSilenceLen int64, silenceThresh Volume, seekStep int) [][]int64 {
	silentRanges := DetectSilenceConcurrent(seg, minSilenceLen, silenceThresh, seekStep)

	lenSeg := seg.Duration()
	var nonsilentRanges [][]int64
	// if there is no silence, the whole thing is nonsilent
	if len(silentRanges) == 0 {
		return append(nonsilentRanges, []int64{0, lenSeg})
	}

	// short circuit when the whole audio segment is silent
	if silentRanges[0][0] == 0 && silentRanges[0][1] == lenSeg {
		return nonsilentRanges
	}

	prevEndI := int64(0)
	endI := int64(0)
	for i := range silentRanges {
		nonsilentRanges = append(nonsilentRanges, []int64{prevEndI, silentRanges[i][0]})
		prevEndI = silentRanges[i][1]
		endI = prevEndI
	}

	if endI != lenSeg {
		nonsilentRanges = append(nonsilentRanges, []int64{prevEndI, lenSeg})
	}

	return nonsilentRanges
}

// SplitOnSilenceConcurrent 使用并发优化的静音检测进行音频分割
func SplitOnSilenceConcurrent(seg *AudioSegment, minSilenceLen int64, silenceThresh Volume, keepSilence int, seekStep int) ([]*AudioSegment, [][]float32, error) {

	chunks := []*AudioSegment{}
	var timings [][]float32

	err := checkEmptyAudio(seg)
	if err != nil {
		return chunks, timings, err
	}

	normAudio, _ := seg.derive(seg.RawData())
	normAudio = matchTargetAmp(seg, -20.0)

	// 使用并发版本进行静音检测
	notSilenceRanges := DetectNonsilentConcurrent(normAudio, minSilenceLen, silenceThresh, seekStep)

	startMin := int64(0)

	if len(notSilenceRanges) == 1 {
		chunks = append(chunks, seg)
		timings = append(timings, []float32{0.0, float32(seg.Len())})
		return chunks, timings, nil
	}

	for i := 0; i < len(notSilenceRanges)-1; i++ {
		endMax := notSilenceRanges[i][1] + (notSilenceRanges[i+1][0]-notSilenceRanges[i][1]+1)/2
		startI := max(int(startMin), int(notSilenceRanges[i][0])-keepSilence)
		endI := min(int(endMax), int(notSilenceRanges[i][1])+keepSilence)

		temp1, _ := seg.Slice(int64(startI), int64(endI))
		if temp1 != nil {
			chunks = append(chunks, temp1)
			timings = append(timings, []float32{float32(startI) / 1000, float32(endI) / 1000.0})
		}

		startMin = notSilenceRanges[i][1]
	}

	startI := max(int(startMin), int(notSilenceRanges[len(notSilenceRanges)-1][0])-keepSilence)
	endI := min(int(seg.Duration()), int(notSilenceRanges[len(notSilenceRanges)-1][1])+keepSilence)
	temp2, _ := seg.Slice(int64(startI), int64(endI))
	if temp2 != nil {
		chunks = append(chunks, temp2)
		timings = append(timings, []float32{float32(startI) / 1000, float32(endI) / 1000.0})
	}

	return chunks, timings, nil
}

// SplitAudio 将音频文件按照指定的目标长度在静音处切分成多个片段
// audio: 音频文件
// targetLen: 目标长度(秒)，默认30分钟
// win: 检测窗口大小(秒)，默认60秒
func SplitAudioConcurrent(audio *AudioSegment, targetLen float64, win float64) ([][]float64, error) {
	if targetLen == 0 {
		targetLen = 30 * 60 // 默认30分钟
	}
	if win == 0 {
		win = 60 // 默认60秒
	}

	duration := float64(audio.Duration() / 1000) // 转换为秒
	if duration <= targetLen+win {
		return [][]float64{{0, duration}}, nil
	}

	segments := make([][]float64, 0)
	pos := 0.0
	safeMargin := 0.5 // 静默点前后安全边界，单位秒

	for pos < duration {
		if duration-pos <= targetLen {
			segments = append(segments, []float64{pos, duration})
			break
		}

		threshold := pos + targetLen
		ws := int64((threshold - win) * 1000) // 窗口开始位置，单位毫秒
		we := int64((threshold + win) * 1000) // 窗口结束位置，单位毫秒

		// 切片获取检测区间的音频
		windowAudio, err := audio.Slice(ws, we)
		if err != nil {
			return nil, fmt.Errorf("error slicing audio: %v", err)
		}

		// 检测静音区域
		silenceRegions := DetectSilenceConcurrent(windowAudio, int64(safeMargin*1000), Volume(-30), 1)

		// 将毫秒单位的静音区域转换为秒，并调整偏移
		var validRegions [][]float64
		for _, region := range silenceRegions {
			start := float64(region[0])/1000 + (threshold - win)
			end := float64(region[1])/1000 + (threshold - win)

			// 筛选长度足够且位置适合的静默区域
			if (end-start) >= (safeMargin*2) &&
				threshold <= start+safeMargin &&
				start+safeMargin <= threshold+win {
				validRegions = append(validRegions, []float64{start, end})
			}
		}

		splitAt := threshold
		if len(validRegions) > 0 {
			splitAt = validRegions[0][0] + safeMargin // 在静默区域起始点后0.5秒处切分
		} else {
			fmt.Printf("No valid silence regions found for audio file at %.1fs, using threshold\n", threshold)
		}

		segments = append(segments, []float64{pos, splitAt})
		pos = splitAt
	}

	fmt.Printf("Audio split completed %d segments\n", len(segments))
	return segments, nil
}
