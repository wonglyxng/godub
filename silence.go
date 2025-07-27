package godub

import (
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp"
)

// InvalidFile error type
type InvalidFile struct {
	OriginalError string
}

func (invalidFile *InvalidFile) Error() string {
	return fmt.Sprintf("InvalidFile Error: %v", invalidFile.OriginalError)
}

// Check if audio is empty
func checkEmptyAudio(seg *AudioSegment) error {

	rms := seg.RMS()
	if rms == 0 {
		return &InvalidFile{"Empty file. Check audio"}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func matchTargetAmp(sound *AudioSegment, targetDBFS Volume) *AudioSegment {
	changeInDBFS := targetDBFS - sound.DBFS()
	ret, _ := sound.ApplyGain(changeInDBFS)
	return ret
}

func DetectSilence(seg *AudioSegment, minSilenceLen int64, silenceThresh Volume, seekStep int) [][]int64 {
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

	for _, i := range sliceStarts {
		audioSlice, _ := seg.Slice(i, i+minSilenceLen)
		if audioSlice.RMS() <= silThresh {
			silenceStarts = append(silenceStarts, i)

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

		if continuous == false && silenceHasGap == true {

			silentRanges = append(silentRanges, []int64{currentRangeStart, prevI + minSilenceLen})
			currentRangeStart = silenceStartI
		}
		prevI = silenceStartI

	}
	silentRanges = append(silentRanges, []int64{currentRangeStart, prevI + minSilenceLen})

	return silentRanges
}

func DetectNonsilent(seg *AudioSegment, minSilenceLen int64, silenceThresh Volume, seekStep int) [][]int64 {

	silentRanges := DetectSilence(seg, minSilenceLen, silenceThresh, seekStep)

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

	if cmp.Equal(nonsilentRanges[0], []time.Duration{0, 0}) {
		nonsilentRanges = nonsilentRanges[1:]
	}

	return nonsilentRanges
}

// SplitOnSilence ...
func SplitOnSilence(seg *AudioSegment, minSilenceLen int64, silenceThresh Volume, keepSilence int, seekStep int) ([]*AudioSegment, [][]float32, error) {

	chunks := []*AudioSegment{}
	var timings [][]float32

	err := checkEmptyAudio(seg)

	if err != nil {
		return chunks, timings, err
	}

	normAudio, _ := seg.derive(seg.RawData())

	normAudio = matchTargetAmp(seg, -20.0)

	notSilenceRanges := DetectNonsilent(normAudio, minSilenceLen, silenceThresh, seekStep)

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

func detectLeadingSilence(sound *AudioSegment, silenceThreshold Volume, chunkSize int) int64 {
	trimMS := int64(0)
	for trimMS < sound.Duration() {
		temp1, _ := sound.Slice(trimMS, trimMS+int64(chunkSize))
		if temp1.DBFS() < silenceThreshold {
			trimMS += int64(chunkSize)
		} else {
			break
		}
	}

	return trimMS
}
