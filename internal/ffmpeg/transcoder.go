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

var detectedEncoder string

func DetectBestH264Encoder() string {
	if detectedEncoder != "" {
		return detectedEncoder
	}

	candidates := []struct {
		name string
		desc string
	}{
		{"h264_qsv", "Intel Quick Sync Video (GPU)"},
		{"h264_nvenc", "NVIDIA NVENC (GPU)"},
		{"h264_amf", "AMD AMF (GPU)"},
	}

	for _, c := range candidates {
		cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=black:s=64x64:d=0.04", "-c:v", c.name, "-f", "null", "-")
		if err := cmd.Run(); err == nil {
			log.Printf("[FFmpeg] Kích hoạt tăng tốc phần cứng GPU: %s (%s)", c.name, c.desc)
			detectedEncoder = c.name
			return detectedEncoder
		}
	}

	log.Println("[FFmpeg] Không tìm thấy card hỗ trợ, chuyển sang CPU (libx264)")
	detectedEncoder = "libx264"
	return detectedEncoder
}

func encodeVideoWithFallback(inputPath, outputPath string, targetHeight int, bitrate, maxrate, bufsize, crf, globalQ string) error {
	enc := DetectBestH264Encoder()

	runEncode := func(encoder string) error {
		var args []string
		args = append(args, "-y", "-i", inputPath)

		switch encoder {
		case "h264_qsv":
			args = append(args,
				"-vf", fmt.Sprintf("scale=-2:%d,format=nv12", targetHeight),
				"-c:v", "h264_qsv",
				"-preset", "faster",
				"-global_quality", globalQ,
				"-b:v", bitrate,
				"-maxrate", maxrate,
				"-bufsize", bufsize,
			)
		case "h264_nvenc":
			args = append(args,
				"-vf", fmt.Sprintf("scale=-2:%d", targetHeight),
				"-c:v", "h264_nvenc",
				"-preset", "p4",
				"-b:v", bitrate,
				"-maxrate", maxrate,
				"-bufsize", bufsize,
			)
		case "h264_amf":
			args = append(args,
				"-vf", fmt.Sprintf("scale=-2:%d", targetHeight),
				"-c:v", "h264_amf",
				"-b:v", bitrate,
				"-maxrate", maxrate,
				"-bufsize", bufsize,
			)
		default: // libx264
			args = append(args,
				"-vf", fmt.Sprintf("scale=-2:%d", targetHeight),
				"-c:v", "libx264",
				"-crf", crf,
				"-preset", "veryfast",
				"-b:v", bitrate,
				"-maxrate", maxrate,
				"-bufsize", bufsize,
			)
		}

		args = append(args, "-c:a", "copy", outputPath)

		cmd := exec.Command("ffmpeg", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	err := runEncode(enc)
	if err != nil && enc != "libx264" {
		// ponytail: fallback sang CPU libx264 nếu hardware encoder gặp format video lạ
		log.Printf("[FFmpeg] Hardware encoder %s gặp lỗi: %v. Đang tự động chuyển sang CPU libx264...", enc, err)
		return runEncode("libx264")
	}
	return err
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
		if stat, err := os.Stat(out1080); err == nil && stat.Size() > 1024*100 {
			log.Printf("[FFmpeg] Phát hiện bản 1080p đã tồn tại (%d MB), bỏ qua bước encode!", stat.Size()/(1024*1024))
			results["1080p"] = out1080
		} else {
			log.Printf("[FFmpeg] Encoding 1080p -> %s...", out1080)
			if err := encodeVideoWithFallback(inputPath, out1080, 1080, "2800k", "3500k", "5000k", "22", "23"); err != nil {
				log.Printf("[FFmpeg] Warning encoding 1080p failed: %v", err)
			} else {
				results["1080p"] = out1080
			}
		}
	} else {
		log.Printf("[FFmpeg] Original height (%dp) < 1080p, skipping 1080p transcoding.", height)
	}

	// 720p
	if height >= 720 {
		out720 := filepath.Join(outDir, "transcode_720p.mp4")
		if stat, err := os.Stat(out720); err == nil && stat.Size() > 1024*100 {
			log.Printf("[FFmpeg] Phát hiện bản 720p đã tồn tại (%d MB), bỏ qua bước encode!", stat.Size()/(1024*1024))
			results["720p"] = out720
		} else {
			log.Printf("[FFmpeg] Encoding 720p -> %s...", out720)
			if err := encodeVideoWithFallback(inputPath, out720, 720, "1400k", "1800k", "2500k", "23", "25"); err != nil {
				log.Printf("[FFmpeg] Warning encoding 720p failed: %v", err)
			} else {
				results["720p"] = out720
			}
		}
	} else {
		log.Printf("[FFmpeg] Original height (%dp) < 720p, skipping 720p transcoding.", height)
	}

	return results, nil
}
