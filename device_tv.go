package main

// Extra framebuffer helpers for sharing the screen with ffplay: ffplay (SDL)
// page-flips between two buffers, so before drawing we pan back to page 0,
// and to animate the picture shrinking at power-off we read back whichever
// page ffplay left on screen.

import (
	"unsafe"
)

// yoffset returns the currently displayed row offset (0 or H for page 2).
func (fb *Framebuffer) yoffset() int {
	var v [40]uint32
	if ioctl(fb.f.Fd(), fbioGetVScreenInfo, unsafe.Pointer(&v[0])) != nil {
		return 0
	}
	return int(v[5])
}

// Reset pans the display back to page 0, which is where Present draws.
func (fb *Framebuffer) Reset() {
	var v [40]uint32
	if ioctl(fb.f.Fd(), fbioGetVScreenInfo, unsafe.Pointer(&v[0])) != nil {
		return
	}
	if v[5] != 0 || v[4] != 0 || v[6] != 32 {
		v[4], v[5], v[6] = 0, 0, 32
		_ = ioctl(fb.f.Fd(), fbioPutVScreenInfo, unsafe.Pointer(&v[0]))
	}
}

// Capture copies what is on screen right now (any page) into the canvas,
// undoing the panel rotation.
func (fb *Framebuffer) Capture(c *Canvas) bool {
	yo := fb.yoffset()
	if (yo+fb.H)*fb.stride > len(fb.mem) {
		yo = 0
	}
	w, h := fb.W, fb.H
	if c.W < w || c.H < h {
		return false
	}
	for y := 0; y < h; y++ {
		sy := y
		if fb.flip {
			sy = h - 1 - y
		}
		src := fb.mem[(yo+sy)*fb.stride:]
		dst := c.Pix[y*c.Stride:]
		for x := 0; x < w; x++ {
			sx := x
			if fb.flip {
				sx = w - 1 - x
			}
			s := sx * 4
			d := x * 4
			dst[d], dst[d+1], dst[d+2], dst[d+3] = src[s+2], src[s+1], src[s], 255
		}
	}
	return true
}

// PresentFit shows a canvas of any size on the panel, scaling it when the
// panel isn't 640x480 (the Mini V4 is 750x560), so text keeps its size.
func (fb *Framebuffer) PresentFit(c *Canvas) {
	if c.W == fb.W && c.H == fb.H {
		fb.Present(c)
		return
	}
	if fb.scaled == nil || fb.scaled.W != fb.W || fb.scaled.H != fb.H {
		fb.scaled = NewCanvas(fb.W, fb.H)
	}
	scaleInto(fb.scaled, c)
	fb.Present(fb.scaled)
}

// CaptureFit reads the screen into a canvas of any size.
func (fb *Framebuffer) CaptureFit(c *Canvas) bool {
	if c.W == fb.W && c.H == fb.H {
		return fb.Capture(c)
	}
	full := NewCanvas(fb.W, fb.H)
	if !fb.Capture(full) {
		return false
	}
	scaleInto(c, full)
	return true
}

// scaleInto resizes src onto dst (bilinear: small upscales stay smooth).
func scaleInto(dst, src *Canvas) {
	sx := float64(src.W-1) / float64(max(dst.W-1, 1))
	sy := float64(src.H-1) / float64(max(dst.H-1, 1))
	for y := 0; y < dst.H; y++ {
		fy := float64(y) * sy
		y0 := int(fy)
		y1 := min(y0+1, src.H-1)
		wy := fy - float64(y0)
		r0 := src.Pix[y0*src.Stride:]
		r1 := src.Pix[y1*src.Stride:]
		d := dst.Pix[y*dst.Stride:]
		for x := 0; x < dst.W; x++ {
			fx := float64(x) * sx
			x0 := int(fx)
			x1 := min(x0+1, src.W-1)
			wx := fx - float64(x0)
			for k := 0; k < 3; k++ {
				a := float64(r0[x0*4+k])*(1-wx) + float64(r0[x1*4+k])*wx
				b := float64(r1[x0*4+k])*(1-wx) + float64(r1[x1*4+k])*wx
				d[x*4+k] = uint8(a*(1-wy) + b*wy + 0.5)
			}
			d[x*4+3] = 255
		}
	}
}
