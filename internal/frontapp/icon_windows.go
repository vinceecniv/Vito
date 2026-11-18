//go:build windows

package frontapp

import (
	"bytes"
	"image"
	"image/png"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32           = windows.NewLazySystemDLL("shell32.dll")
	user32            = windows.NewLazySystemDLL("user32.dll")
	gdi32             = windows.NewLazySystemDLL("gdi32.dll")
	pSHDefExtractIcon = shell32.NewProc("SHDefExtractIconW")
	pGetIconInfo      = user32.NewProc("GetIconInfo")
	pDestroyIcon      = user32.NewProc("DestroyIcon")
	pGetObject        = gdi32.NewProc("GetObjectW")
	pGetDIBits        = gdi32.NewProc("GetDIBits")
	pDeleteObject     = gdi32.NewProc("DeleteObject")
	pCreateCompatDC   = gdi32.NewProc("CreateCompatibleDC")
	pDeleteDC         = gdi32.NewProc("DeleteDC")
)

type iconInfo struct {
	fIcon              int32
	xHotspot, yHotspot uint32
	hbmMask, hbmColor  windows.Handle
}

type bitmap struct {
	bmType, bmWidth, bmHeight, bmWidthBytes int32
	bmPlanes, bmBitsPixel                   uint16
	bmBits                                  uintptr
}

type bitmapInfo struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, sizeImage uint32
	xPels, yPels           int32
	clrUsed, clrImportant  uint32
	colors                 [4]uint32 // room for what GetDIBits may write
}

// iconData draws the executable's first icon at 64 pixels into a PNG.
func iconData(exe string) ([]byte, string) {
	path, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return nil, ""
	}
	var large windows.Handle
	if r, _, _ := pSHDefExtractIcon.Call(uintptr(unsafe.Pointer(path)), 0, 0,
		uintptr(unsafe.Pointer(&large)), 0, uintptr(64|16<<16)); r != 0 || large == 0 {
		return nil, ""
	}
	defer pDestroyIcon.Call(uintptr(large))
	var ii iconInfo
	if r, _, _ := pGetIconInfo.Call(uintptr(large), uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil, ""
	}
	defer pDeleteObject.Call(uintptr(ii.hbmMask))
	if ii.hbmColor == 0 {
		return nil, ""
	}
	defer pDeleteObject.Call(uintptr(ii.hbmColor))
	var bm bitmap
	if r, _, _ := pGetObject.Call(uintptr(ii.hbmColor), unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 || bm.bmWidth <= 0 || bm.bmHeight <= 0 {
		return nil, ""
	}
	w, h := int(bm.bmWidth), int(bm.bmHeight)
	dc, _, _ := pCreateCompatDC.Call(0)
	if dc == 0 {
		return nil, ""
	}
	defer pDeleteDC.Call(dc)
	read := func(hbm windows.Handle) []byte {
		buf := make([]byte, w*h*4)
		bi := bitmapInfo{size: 40, width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
		if r, _, _ := pGetDIBits.Call(dc, uintptr(hbm), 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0); r == 0 {
			return nil
		}
		return buf
	}
	px := read(ii.hbmColor)
	if px == nil {
		return nil, ""
	}
	hasAlpha := false
	for i := 3; i < len(px); i += 4 {
		if px[i] != 0 {
			hasAlpha = true
			break
		}
	}
	var mask []byte
	if !hasAlpha && ii.hbmMask != 0 {
		mask = read(ii.hbmMask) // white where the icon is transparent
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		b, g, r, a := px[i*4], px[i*4+1], px[i*4+2], px[i*4+3]
		if !hasAlpha {
			a = 255
			if mask != nil && mask[i*4] != 0 {
				a = 0
			}
		}
		copy(img.Pix[i*4:], []byte{r, g, b, a})
	}
	var out bytes.Buffer
	if png.Encode(&out, img) != nil {
		return nil, ""
	}
	return out.Bytes(), ".png"
}
