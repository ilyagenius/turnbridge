package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"log"
	"sort"
	"strconv"
	"strings"
)

const (
	sliderCaptchaType     = "slider"
	defaultSliderAttempts = 4
)

type sliderCaptchaContent struct {
	Image    image.Image
	Size     int
	Steps    []int
	Attempts int
}

type sliderCandidate struct {
	Index       int
	ActiveSteps []int
	Score       int64
}

func parseSliderCaptchaContentResponse(resp map[string]interface{}) (*sliderCaptchaContent, error) {
	respObj, ok := resp["response"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid slider content response: %v", resp)
	}

	status, _ := respObj["status"].(string)
	if status != "OK" {
		return nil, fmt.Errorf("slider getContent status: %s", status)
	}

	extension, _ := respObj["extension"].(string)
	extension = strings.ToLower(extension)
	if extension != "jpeg" && extension != "jpg" {
		return nil, fmt.Errorf("unsupported slider image format: %s", extension)
	}

	rawImage, _ := respObj["image"].(string)
	if rawImage == "" {
		return nil, fmt.Errorf("slider image missing")
	}

	rawSteps, ok := respObj["steps"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("slider steps missing")
	}

	steps, err := parseIntSlice(rawSteps)
	if err != nil {
		return nil, err
	}

	size, swaps, attempts, err := parseSliderSteps(steps)
	if err != nil {
		return nil, err
	}

	img, err := decodeSliderImage(rawImage)
	if err != nil {
		return nil, err
	}

	return &sliderCaptchaContent{
		Image:    img,
		Size:     size,
		Steps:    swaps,
		Attempts: attempts,
	}, nil
}

func parseIntSlice(raw []interface{}) ([]int, error) {
	values := make([]int, 0, len(raw))
	for _, item := range raw {
		number, err := parseIntValue(item)
		if err != nil {
			return nil, err
		}
		values = append(values, number)
	}
	return values, nil
}

func parseIntValue(raw interface{}) (int, error) {
	switch value := raw.(type) {
	case float64:
		return int(value), nil
	case int:
		return value, nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, fmt.Errorf("invalid numeric value: %v", raw)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("invalid numeric value: %v", raw)
	}
}

func parseSliderSteps(steps []int) (int, []int, int, error) {
	if len(steps) < 3 {
		return 0, nil, 0, fmt.Errorf("slider steps payload too short")
	}

	size := steps[0]
	if size <= 0 {
		return 0, nil, 0, fmt.Errorf("invalid slider size: %d", size)
	}

	remaining := append([]int(nil), steps[1:]...)
	attempts := defaultSliderAttempts
	if len(remaining)%2 != 0 {
		attempts = remaining[len(remaining)-1]
		remaining = remaining[:len(remaining)-1]
	}
	if attempts <= 0 {
		attempts = defaultSliderAttempts
	}
	if len(remaining) == 0 || len(remaining)%2 != 0 {
		return 0, nil, 0, fmt.Errorf("invalid slider swap payload")
	}

	return size, remaining, attempts, nil
}

func decodeSliderImage(rawImage string) (image.Image, error) {
	decoded, err := base64.StdEncoding.DecodeString(rawImage)
	if err != nil {
		return nil, fmt.Errorf("decode slider image: %w", err)
	}

	img, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		return nil, fmt.Errorf("decode slider image: %w", err)
	}

	return img, nil
}

func encodeSliderAnswer(activeSteps []int) (string, error) {
	payload := struct {
		Value []int `json:"value"`
	}{
		Value: activeSteps,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

func buildSliderActiveSteps(swaps []int, candidateIndex int) []int {
	if candidateIndex <= 0 {
		return []int{}
	}

	end := candidateIndex * 2
	if end > len(swaps) {
		end = len(swaps)
	}

	return append([]int(nil), swaps[:end]...)
}

func buildSliderTileMapping(gridSize int, activeSteps []int) ([]int, error) {
	tileCount := gridSize * gridSize
	if tileCount <= 0 {
		return nil, fmt.Errorf("invalid slider tile count: %d", tileCount)
	}
	if len(activeSteps)%2 != 0 {
		return nil, fmt.Errorf("invalid active steps length: %d", len(activeSteps))
	}

	mapping := make([]int, tileCount)
	for i := range mapping {
		mapping[i] = i
	}

	for idx := 0; idx < len(activeSteps); idx += 2 {
		left := activeSteps[idx]
		right := activeSteps[idx+1]
		if left < 0 || right < 0 || left >= tileCount || right >= tileCount {
			return nil, fmt.Errorf("slider step out of range: %d,%d", left, right)
		}
		mapping[left], mapping[right] = mapping[right], mapping[left]
	}

	return mapping, nil
}

func rankSliderCandidates(img image.Image, gridSize int, swaps []int) ([]sliderCandidate, error) {
	candidateCount := len(swaps) / 2
	if candidateCount == 0 {
		return nil, fmt.Errorf("slider has no candidates")
	}

	candidates := make([]sliderCandidate, 0, candidateCount)
	for idx := 1; idx <= candidateCount; idx++ {
		activeSteps := buildSliderActiveSteps(swaps, idx)
		mapping, err := buildSliderTileMapping(gridSize, activeSteps)
		if err != nil {
			return nil, err
		}

		score, err := scoreSliderCandidate(img, gridSize, mapping)
		if err != nil {
			return nil, err
		}

		candidates = append(candidates, sliderCandidate{
			Index:       idx,
			ActiveSteps: activeSteps,
			Score:       score,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Index < candidates[j].Index
		}
		return candidates[i].Score < candidates[j].Score
	})

	return candidates, nil
}

func scoreSliderCandidate(img image.Image, gridSize int, mapping []int) (int64, error) {
	rendered, err := renderSliderCandidate(img, gridSize, mapping)
	if err != nil {
		return 0, err
	}

	return scoreRenderedSliderImage(rendered, gridSize), nil
}

func renderSliderCandidate(img image.Image, gridSize int, mapping []int) (*image.RGBA, error) {
	if gridSize <= 0 {
		return nil, fmt.Errorf("invalid grid size: %d", gridSize)
	}

	tileCount := gridSize * gridSize
	if len(mapping) != tileCount {
		return nil, fmt.Errorf("unexpected tile mapping length: %d", len(mapping))
	}

	bounds := img.Bounds()
	rendered := image.NewRGBA(bounds)
	for dstIndex, srcIndex := range mapping {
		srcRect := sliderTileRect(bounds, gridSize, srcIndex)
		dstRect := sliderTileRect(bounds, gridSize, dstIndex)
		copyScaledTile(rendered, dstRect, img, srcRect)
	}

	return rendered, nil
}

func scoreRenderedSliderImage(img image.Image, gridSize int) int64 {
	bounds := img.Bounds()
	var score int64

	for row := 0; row < gridSize; row++ {
		for col := 0; col < gridSize-1; col++ {
			leftRect := sliderTileRect(bounds, gridSize, row*gridSize+col)
			rightRect := sliderTileRect(bounds, gridSize, row*gridSize+col+1)
			height := minInt(leftRect.Dy(), rightRect.Dy())
			for offset := 0; offset < height; offset++ {
				score += pixelDiff(
					img.At(leftRect.Max.X-1, leftRect.Min.Y+offset),
					img.At(rightRect.Min.X, rightRect.Min.Y+offset),
				)
			}
		}
	}

	for row := 0; row < gridSize-1; row++ {
		for col := 0; col < gridSize; col++ {
			topRect := sliderTileRect(bounds, gridSize, row*gridSize+col)
			bottomRect := sliderTileRect(bounds, gridSize, (row+1)*gridSize+col)
			width := minInt(topRect.Dx(), bottomRect.Dx())
			for offset := 0; offset < width; offset++ {
				score += pixelDiff(
					img.At(topRect.Min.X+offset, topRect.Max.Y-1),
					img.At(bottomRect.Min.X+offset, bottomRect.Min.Y),
				)
			}
		}
	}

	return score
}

func sliderTileRect(bounds image.Rectangle, gridSize int, index int) image.Rectangle {
	row := index / gridSize
	col := index % gridSize

	x0 := bounds.Min.X + col*bounds.Dx()/gridSize
	x1 := bounds.Min.X + (col+1)*bounds.Dx()/gridSize
	y0 := bounds.Min.Y + row*bounds.Dy()/gridSize
	y1 := bounds.Min.Y + (row+1)*bounds.Dy()/gridSize

	return image.Rect(x0, y0, x1, y1)
}

func copyScaledTile(dst *image.RGBA, dstRect image.Rectangle, src image.Image, srcRect image.Rectangle) {
	if dstRect.Empty() || srcRect.Empty() {
		return
	}

	dstWidth := dstRect.Dx()
	dstHeight := dstRect.Dy()
	srcWidth := srcRect.Dx()
	srcHeight := srcRect.Dy()

	for y := 0; y < dstHeight; y++ {
		sy := srcRect.Min.Y + y*srcHeight/dstHeight
		for x := 0; x < dstWidth; x++ {
			sx := srcRect.Min.X + x*srcWidth/dstWidth
			dst.Set(dstRect.Min.X+x, dstRect.Min.Y+y, src.At(sx, sy))
		}
	}
}

func pixelDiff(left color.Color, right color.Color) int64 {
	lr, lg, lb, _ := left.RGBA()
	rr, rg, rb, _ := right.RGBA()

	return absDiff(lr, rr) + absDiff(lg, rg) + absDiff(lb, rb)
}

func absDiff(left uint32, right uint32) int64 {
	if left > right {
		return int64(left - right)
	}
	return int64(right - left)
}

func trySliderCaptchaCandidates(
	candidates []sliderCandidate,
	maxAttempts int,
	check func(candidate sliderCandidate) (*captchaCheckResult, error),
) (string, error) {
	if len(candidates) == 0 {
		return "", fmt.Errorf("slider has no ranked candidates")
	}

	limit := minInt(maxAttempts, len(candidates))
	if limit <= 0 {
		return "", fmt.Errorf("slider has no attempts available")
	}

	for idx := 0; idx < limit; idx++ {
		result, err := check(candidates[idx])
		if err != nil {
			return "", err
		}

		switch result.Status {
		case "OK":
			if result.SuccessToken == "" {
				return "", fmt.Errorf("success_token not found")
			}
			return result.SuccessToken, nil
		case "ERROR_LIMIT":
			return "", fmt.Errorf("slider check status: %s", result.Status)
		default:
			log.Printf("[Captcha] Slider attempt %d/%d: status=%s, trying next...", idx+1, limit, result.Status)
			continue
		}
	}

	return "", fmt.Errorf("slider guesses exhausted")
}

func describeCaptchaTypes(settingsByType map[string]string) string {
	if len(settingsByType) == 0 {
		return "none"
	}

	types := make([]string, 0, len(settingsByType))
	for captchaType := range settingsByType {
		types = append(types, captchaType)
	}
	sort.Strings(types)
	return strings.Join(types, ",")
}
