package rfb

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"math/rand/v2"
	"strconv"
	"testing"
)

func TestZRLEContinuousScrollingAtEveryCompressionLevel(t *testing.T) {
	for _, level := range []int{zlib.NoCompression, zlib.BestSpeed, zlib.DefaultCompression, zlib.BestCompression, zlib.HuffmanOnly} {
		t.Run(strconv.Itoa(level), func(t *testing.T) {
			stream := &zrleStream{}
			var err error
			stream.writer, err = zlib.NewWriterLevel(&stream.buffer, level)
			if err != nil {
				t.Fatal(err)
			}
			sink := scenario(t, 128, 128, func(s *server, push func([]byte)) {
				for frame := 0; frame < 40; frame++ {
					var tiles []byte
					for tile := 0; tile < 4; tile++ {
						tiles = append(tiles, 0) // raw 64x64 tile
						for pixel := 0; pixel < 64*64; pixel++ {
							tiles = append(tiles, byte(pixel*31+frame), byte(pixel>>3), byte(tile+frame))
						}
					}
					push(update(stream.rect(0, 0, 128, 128, tiles)))
				}
			})
			if got := sink.at(0, 0); got != [3]byte{39, 0, 39} {
				t.Fatalf("continuous scrolling ended on a stale pixel: %v", got)
			}
		})
	}
}

func TestZRLEVariedRectanglesKeepTheirCompressionHistory(t *testing.T) {
	for _, level := range []int{zlib.NoCompression, zlib.BestSpeed, zlib.DefaultCompression, zlib.BestCompression, zlib.HuffmanOnly} {
		t.Run(strconv.Itoa(level), func(t *testing.T) {
			stream := &zrleStream{}
			stream.writer, _ = zlib.NewWriterLevel(&stream.buffer, level)
			random := rand.New(rand.NewPCG(17, 83))
			client := clientOver(nil, 257, 257)
			sink := &canvas{}
			sink.Resize(257, 257)
			for frame := 0; frame < 250; frame++ {
				width, height := 1+random.IntN(257), 1+random.IntN(257)
				var tiles []byte
				var expected [3]byte
				for top := 0; top < height; top += 64 {
					for left := 0; left < width; left += 64 {
						if frame%3 == 0 {
							expected = [3]byte{byte(frame), byte(left), byte(top)}
							tiles = append(tiles, 1)
							tiles = append(tiles, expected[:]...)
						} else {
							tiles = append(tiles, 0)
							for pixel := 0; pixel < min(64, height-top)*min(64, width-left); pixel++ {
								expected = [3]byte{byte(random.Uint32()), byte(random.Uint32()), byte(random.Uint32())}
								tiles = append(tiles, expected[:]...)
							}
						}
					}
				}
				packet := stream.rect(0, 0, width, height, tiles)
				client.r = bufio.NewReader(bytes.NewReader(packet[12:]))
				if err := client.decodeZRLE(sink, 0, 0, width, height); err != nil {
					t.Fatalf("frame %d, %dx%d: %v", frame, width, height, err)
				}
				if got := sink.at(width-1, height-1); got != expected {
					t.Fatalf("frame %d, %dx%d: pixel %v, want %v", frame, width, height, got, expected)
				}
			}
		})
	}
}
