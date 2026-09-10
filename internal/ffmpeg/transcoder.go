package ffmpeg

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

type ProbeResult struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

func CheckFFmpeg() error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}
	return nil
}

func GetVideoHeight(filePath string) (int, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,codec_type",
		"-of", "json",
		filePath)

	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe failed: %w", err)
	}

	var res ProbeResult
	if err := json.Unmarshal(out, &res); err != nil {
		return 0, err
	}

	for _, s := range res.Streams {
		if s.CodecType == "video" && s.Height > 0 {
			return s.Height, nil
		}
	}

	return 0, fmt.Errorf("no video stream found in %s", filePath)
}

// TranscodeAll sinh 3 phiên bản độ phân giải (original, 1080p, 720p)
func TranscodeAll(inputPath, outDir string) (map[string]string, error) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, err
	}

	results := make(map[string]string)
	results["original"] = inputPath

	height, err := GetVideoHeight(inputPath)
	if err != nil {
		log.Printf("[FFmpeg] Warning probing height (%v). Will attempt full transcoding.", err)
		height = 2160
	}

	log.Printf("[FFmpeg] Original video detected height: %dp", height)

	// 1080p
	if height >= 1080 {
		out1080 := filepath.Join(outDir, "transcode_1080p.mp4")
		log.Printf("[FFmpeg] Encoding 1080p -> %s...", out1080)
		cmd := exec.Command("ffmpeg", "-y", "-i", inputPath,
			"-vf", "scale=-2:1080",
			"-c:v", "libx264", "-crf", "22", "-preset", "medium",
			"-b:v", "2800k", "-maxrate", "3500k", "-bufsize", "5000k",
			"-c:a", "copy",
			out1080)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("[FFmpeg] Warning encoding 1080p failed: %v", err)
		} else {
			results["1080p"] = out1080
		}
	} else {
		log.Printf("[FFmpeg] Original height (%dp) < 1080p, skipping 1080p transcoding.", height)
	}

	// 720p
	if height >= 720 {
		out720 := filepath.Join(outDir, "transcode_720p.mp4")
		log.Printf("[FFmpeg] Encoding 720p -> %s...", out720)
		cmd := exec.Command("ffmpeg", "-y", "-i", inputPath,
			"-vf", "scale=-2:720",
			"-c:v", "libx264", "-crf", "23", "-preset", "medium",
			"-b:v", "1400k", "-maxrate", "1800k", "-bufsize", "2500k",
			"-c:a", "copy",
			out720)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("[FFmpeg] Warning encoding 720p failed: %v", err)
		} else {
			results["720p"] = out720
		}
	} else {
		log.Printf("[FFmpeg] Original height (%dp) < 720p, skipping 720p transcoding.", height)
	}

	return results, nil
}
