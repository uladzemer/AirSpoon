package dxgi

// DXGI_MODE_ROTATION. Rotation maps native surface pixels to the upright
// desktop clockwise; UNSPECIFIED (0) is treated as IDENTITY.
const (
	dxgiModeRotationIdentity = 1
	dxgiModeRotation90       = 2
	dxgiModeRotation180      = 3
	dxgiModeRotation270      = 4
)

func rotatedSize(w, h int, rotation uint32) (int, int) {
	if rotation == dxgiModeRotation90 || rotation == dxgiModeRotation270 {
		return h, w
	}
	return w, h
}

func rotatePoint(x, y, w, h int, rotation uint32) (int, int) {
	switch rotation {
	case dxgiModeRotation90:
		return h - 1 - y, x
	case dxgiModeRotation180:
		return w - 1 - x, h - 1 - y
	case dxgiModeRotation270:
		return y, w - 1 - x
	default:
		return x, y
	}
}

func inverseRotation(rotation uint32) uint32 {
	switch rotation {
	case dxgiModeRotation90:
		return dxgiModeRotation270
	case dxgiModeRotation270:
		return dxgiModeRotation90
	default:
		return rotation
	}
}

// desktopRectToSurface transforms an unclipped, half-open desktop rectangle
// into native surface coordinates. Clip afterwards (also for negative origins).
func desktopRectToSurface(r Rect, w, h int, rotation uint32) Rect {
	switch rotation {
	case dxgiModeRotation90:
		return Rect{X: r.Y, Y: h - r.Right(), W: r.H, H: r.W}
	case dxgiModeRotation180:
		return Rect{X: w - r.Right(), Y: h - r.Bottom(), W: r.W, H: r.H}
	case dxgiModeRotation270:
		return Rect{X: w - r.Bottom(), Y: r.X, W: r.H, H: r.W}
	default:
		return r
	}
}

// rotatedBGRASteps describes sampling the upright image directly from a native
// BGRA surface, including its row padding. No full-frame rotation buffer needed.
func rotatedBGRASteps(w, h, stride int, rotation uint32) (base, xStep, yStep int) {
	switch rotation {
	case dxgiModeRotation90:
		return (h - 1) * stride, -stride, 4
	case dxgiModeRotation180:
		return (h-1)*stride + (w-1)*4, -4, -stride
	case dxgiModeRotation270:
		return (w - 1) * 4, stride, -4
	default:
		return 0, 4, stride
	}
}
