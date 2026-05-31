package qrcode

import (
	"fmt"
	"math"
	"strings"
)

// Ecc is the QR error-correction level. Higher levels recover more of a damaged
// or noisy capture at the cost of data capacity. The values are ordered to index
// the eccCodewordsPerBlock and numErrorCorrectionBlocks tables directly.
type Ecc int

const (
	LevelLow Ecc = iota
	LevelMedium
	LevelQuartile
	LevelHigh
)

type Code struct {
	Version int
	Size    int
	modules [][]bool
}

func (c *Code) Module(x, y int) bool {
	return c.modules[y][x]
}

func (level Ecc) String() string {
	switch level {
	case LevelLow:
		return "L"
	case LevelMedium:
		return "M"
	case LevelQuartile:
		return "Q"
	case LevelHigh:
		return "H"
	default:
		return "?"
	}
}

// formatBits returns the 2-bit error-correction indicator stored in the QR
// format information, which is not the same order as the table index.
func (level Ecc) formatBits() int {
	switch level {
	case LevelLow:
		return 1
	case LevelMedium:
		return 0
	case LevelQuartile:
		return 3
	case LevelHigh:
		return 2
	default:
		return 1
	}
}

func ParseEcc(name string) (Ecc, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "L":
		return LevelLow, nil
	case "M":
		return LevelMedium, nil
	case "Q":
		return LevelQuartile, nil
	case "H":
		return LevelHigh, nil
	default:
		return 0, fmt.Errorf("invalid EC level %q (use L, M, Q, or H)", name)
	}
}

func EncodeText(text string, level Ecc, maxVersion int) (*Code, error) {
	return EncodeBytes([]byte(text), level, maxVersion)
}

func EncodeTextAtLeast(text string, level Ecc, minVersion int, maxVersion int) (*Code, error) {
	return EncodeBytesAtLeast([]byte(text), level, minVersion, maxVersion)
}

func EncodeBytes(data []byte, level Ecc, maxVersion int) (*Code, error) {
	return EncodeBytesAtLeast(data, level, 1, maxVersion)
}

func EncodeBytesAtLeast(data []byte, level Ecc, minVersion int, maxVersion int) (*Code, error) {
	if level < LevelLow || level > LevelHigh {
		return nil, fmt.Errorf("invalid EC level %d", level)
	}
	if maxVersion < 1 || maxVersion > 40 {
		return nil, fmt.Errorf("max version must be between 1 and 40")
	}
	if minVersion < 1 || minVersion > maxVersion {
		return nil, fmt.Errorf("min version must be between 1 and max version")
	}

	version := 0
	var dataCodewords int
	for v := minVersion; v <= maxVersion; v++ {
		capacity := numDataCodewords(v, level)
		ccBits := byteModeCharCountBits(v)
		if len(data) < (1<<ccBits) && 4+ccBits+len(data)*8 <= capacity*8 {
			version = v
			dataCodewords = capacity
			break
		}
	}
	if version == 0 {
		return nil, fmt.Errorf("%d bytes exceed QR capacity from version %d to version %d at EC level %s", len(data), minVersion, maxVersion, level)
	}

	bits := makePayloadBits(data, version, dataCodewords)
	codewords := bitsToBytes(bits)
	fullCodewords := addErrorCorrectionAndInterleave(codewords, version, level)

	base := newBuilder(version, level)
	base.drawFunctionPatterns()
	base.drawCodewords(fullCodewords)

	bestMask := 0
	bestPenalty := math.MaxInt
	var best [][]bool
	for mask := 0; mask < 8; mask++ {
		candidate := base.clone()
		candidate.applyMask(mask)
		candidate.drawFormatBits(mask)
		penalty := candidate.penaltyScore()
		if penalty < bestPenalty {
			bestPenalty = penalty
			bestMask = mask
			best = candidate.modules
		}
	}
	_ = bestMask

	return &Code{
		Version: version,
		Size:    17 + 4*version,
		modules: best,
	}, nil
}

func makePayloadBits(data []byte, version int, dataCodewords int) []bool {
	var bits []bool
	appendBits := func(value, count int) {
		for i := count - 1; i >= 0; i-- {
			bits = append(bits, ((value>>i)&1) != 0)
		}
	}

	appendBits(0x4, 4)
	appendBits(len(data), byteModeCharCountBits(version))
	for _, b := range data {
		appendBits(int(b), 8)
	}

	capacityBits := dataCodewords * 8
	terminator := capacityBits - len(bits)
	if terminator > 4 {
		terminator = 4
	}
	appendBits(0, terminator)
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	for pad := 0; len(bits) < capacityBits; pad++ {
		if pad%2 == 0 {
			appendBits(0xEC, 8)
		} else {
			appendBits(0x11, 8)
		}
	}
	return bits
}

func bitsToBytes(bits []bool) []byte {
	result := make([]byte, len(bits)/8)
	for i, bit := range bits {
		if bit {
			result[i/8] |= 1 << uint(7-i%8)
		}
	}
	return result
}

func byteModeCharCountBits(version int) int {
	if version <= 9 {
		return 8
	}
	return 16
}

func numDataCodewords(version int, level Ecc) int {
	return numRawDataCodewords(version) - eccCodewordsPerBlock[level][version]*numErrorCorrectionBlocks[level][version]
}

func numRawDataCodewords(version int) int {
	result := (16*version+128)*version + 64
	if version >= 2 {
		numAlign := version/7 + 2
		result -= (25*numAlign-10)*numAlign - 55
		if version >= 7 {
			result -= 36
		}
	}
	return result / 8
}

// Indexed by [Ecc][version]; version 0 is unused padding. Values follow the
// QR Code specification (ISO/IEC 18004) error-correction characteristics.
var eccCodewordsPerBlock = [4][41]int{
	{-1, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18, 20, 24, 26, 30, 22, 24, 28, 30, 28, 28, 28, 28, 30, 30, 26, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
	{-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28},
	{-1, 13, 22, 18, 26, 18, 24, 18, 22, 20, 24, 28, 26, 24, 20, 30, 24, 28, 28, 26, 30, 28, 30, 30, 30, 30, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
	{-1, 17, 28, 22, 16, 22, 28, 26, 26, 24, 28, 24, 28, 22, 24, 24, 30, 28, 28, 26, 28, 30, 24, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
}

// Indexed by [Ecc][version]; version 0 is unused padding.
var numErrorCorrectionBlocks = [4][41]int{
	{-1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12, 12, 12, 13, 14, 15, 16, 17, 18, 19, 19, 20, 21, 22, 24, 25},
	{-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49},
	{-1, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8, 8, 10, 12, 16, 12, 17, 16, 18, 21, 20, 23, 23, 25, 27, 29, 34, 34, 35, 38, 40, 43, 45, 48, 51, 53, 56, 59, 62, 65, 68},
	{-1, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8, 11, 11, 16, 16, 18, 16, 19, 21, 25, 25, 25, 34, 30, 32, 35, 37, 40, 42, 45, 48, 51, 54, 57, 60, 63, 66, 70, 74, 77, 81},
}

func addErrorCorrectionAndInterleave(data []byte, version int, level Ecc) []byte {
	numBlocks := numErrorCorrectionBlocks[level][version]
	blockEccLen := eccCodewordsPerBlock[level][version]
	rawCodewords := numRawDataCodewords(version)
	numShortBlocks := numBlocks - rawCodewords%numBlocks
	shortBlockLen := rawCodewords / numBlocks
	divisor := reedSolomonDivisor(blockEccLen)

	blocks := make([][]byte, numBlocks)
	k := 0
	for i := 0; i < numBlocks; i++ {
		dataLen := shortBlockLen - blockEccLen
		if i >= numShortBlocks {
			dataLen++
		}
		dat := append([]byte(nil), data[k:k+dataLen]...)
		k += dataLen
		ecc := reedSolomonRemainder(dat, divisor)
		if i < numShortBlocks {
			dat = append(dat, 0)
		}
		blocks[i] = append(dat, ecc...)
	}

	result := make([]byte, 0, rawCodewords)
	for i := 0; i < len(blocks[0]); i++ {
		for j, block := range blocks {
			if i != shortBlockLen-blockEccLen || j >= numShortBlocks {
				result = append(result, block[i])
			}
		}
	}
	return result
}

func reedSolomonDivisor(degree int) []byte {
	result := make([]byte, degree)
	result[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := 0; j < len(result); j++ {
			result[j] = gfMultiply(result[j], root)
			if j+1 < len(result) {
				result[j] ^= result[j+1]
			}
		}
		root = gfMultiply(root, 0x02)
	}
	return result
}

func reedSolomonRemainder(data []byte, divisor []byte) []byte {
	result := make([]byte, len(divisor))
	for _, b := range data {
		factor := b ^ result[0]
		copy(result, result[1:])
		result[len(result)-1] = 0
		for i, coef := range divisor {
			result[i] ^= gfMultiply(coef, factor)
		}
	}
	return result
}

func gfMultiply(x, y byte) byte {
	var z int
	a := int(x)
	b := int(y)
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		if ((b >> i) & 1) != 0 {
			z ^= a
		}
	}
	return byte(z)
}

type builder struct {
	version    int
	level      Ecc
	size       int
	modules    [][]bool
	isFunction [][]bool
}

func newBuilder(version int, level Ecc) *builder {
	size := 17 + 4*version
	modules := make([][]bool, size)
	isFunction := make([][]bool, size)
	for i := range modules {
		modules[i] = make([]bool, size)
		isFunction[i] = make([]bool, size)
	}
	return &builder{
		version:    version,
		level:      level,
		size:       size,
		modules:    modules,
		isFunction: isFunction,
	}
}

func (b *builder) clone() *builder {
	modules := make([][]bool, b.size)
	isFunction := make([][]bool, b.size)
	for y := 0; y < b.size; y++ {
		modules[y] = append([]bool(nil), b.modules[y]...)
		isFunction[y] = append([]bool(nil), b.isFunction[y]...)
	}
	return &builder{
		version:    b.version,
		level:      b.level,
		size:       b.size,
		modules:    modules,
		isFunction: isFunction,
	}
}

func (b *builder) drawFunctionPatterns() {
	for _, pos := range [][2]int{{3, 3}, {b.size - 4, 3}, {3, b.size - 4}} {
		b.drawFinder(pos[0], pos[1])
	}
	for i := 0; i < b.size; i++ {
		if !b.isFunction[6][i] {
			b.setFunction(i, 6, i%2 == 0)
		}
		if !b.isFunction[i][6] {
			b.setFunction(6, i, i%2 == 0)
		}
	}
	positions := alignmentPatternPositions(b.version)
	last := len(positions) - 1
	for i, x := range positions {
		for j, y := range positions {
			// Skip only the three patterns that coincide with the finder
			// corners; the rest are placed even where they overlap the timing
			// pattern, which they correctly overwrite.
			if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
				continue
			}
			b.drawAlignment(x, y)
		}
	}
	b.drawFormatBits(0)
	if b.version >= 7 {
		b.drawVersionBits()
	}
}

func (b *builder) drawFinder(cx, cy int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			x := cx + dx
			y := cy + dy
			if x < 0 || y < 0 || x >= b.size || y >= b.size {
				continue
			}
			dist := max(abs(dx), abs(dy))
			b.setFunction(x, y, dist != 2 && dist != 4)
		}
	}
}

func (b *builder) drawAlignment(cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			dist := max(abs(dx), abs(dy))
			b.setFunction(cx+dx, cy+dy, dist != 1)
		}
	}
}

func alignmentPatternPositions(version int) []int {
	if version == 1 {
		return nil
	}
	numAlign := version/7 + 2
	step := (version*8 + numAlign*3 + 5) / (numAlign*4 - 4) * 2
	result := make([]int, numAlign)
	result[0] = 6
	pos := 17 + 4*version - 7
	for i := numAlign - 1; i >= 1; i-- {
		result[i] = pos
		pos -= step
	}
	return result
}

func (b *builder) drawFormatBits(mask int) {
	data := (b.level.formatBits() << 3) | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := ((data << 10) | rem) ^ 0x5412

	for i := 0; i <= 5; i++ {
		b.setFunction(8, i, bit(bits, i))
	}
	b.setFunction(8, 7, bit(bits, 6))
	b.setFunction(8, 8, bit(bits, 7))
	b.setFunction(7, 8, bit(bits, 8))
	for i := 9; i < 15; i++ {
		b.setFunction(14-i, 8, bit(bits, i))
	}
	for i := 0; i < 8; i++ {
		b.setFunction(b.size-1-i, 8, bit(bits, i))
	}
	for i := 8; i < 15; i++ {
		b.setFunction(8, b.size-15+i, bit(bits, i))
	}
	b.setFunction(8, b.size-8, true)
}

func (b *builder) drawVersionBits() {
	rem := b.version
	for i := 0; i < 12; i++ {
		rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
	}
	bits := (b.version << 12) | rem
	for i := 0; i < 18; i++ {
		a := b.size - 11 + i%3
		c := i / 3
		value := bit(bits, i)
		b.setFunction(a, c, value)
		b.setFunction(c, a, value)
	}
}

func (b *builder) drawCodewords(data []byte) {
	bitIndex := 0
	for right := b.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < b.size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if ((right + 1) & 2) == 0 {
					y = b.size - 1 - vert
				}
				if b.isFunction[y][x] {
					continue
				}
				value := false
				if bitIndex < len(data)*8 {
					value = ((data[bitIndex/8] >> uint(7-bitIndex%8)) & 1) != 0
					bitIndex++
				}
				b.modules[y][x] = value
			}
		}
	}
}

func (b *builder) applyMask(mask int) {
	for y := 0; y < b.size; y++ {
		for x := 0; x < b.size; x++ {
			if !b.isFunction[y][x] && maskApplies(mask, x, y) {
				b.modules[y][x] = !b.modules[y][x]
			}
		}
	}
}

func maskApplies(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (y/2+x/3)%2 == 0
	case 5:
		return (x*y)%2+(x*y)%3 == 0
	case 6:
		return ((x*y)%2+(x*y)%3)%2 == 0
	case 7:
		return ((x+y)%2+(x*y)%3)%2 == 0
	default:
		panic("invalid mask")
	}
}

func (b *builder) penaltyScore() int {
	result := 0

	for y := 0; y < b.size; y++ {
		runColor := b.modules[y][0]
		runLen := 1
		for x := 1; x < b.size; x++ {
			if b.modules[y][x] == runColor {
				runLen++
			} else {
				if runLen >= 5 {
					result += runLen - 2
				}
				runColor = b.modules[y][x]
				runLen = 1
			}
		}
		if runLen >= 5 {
			result += runLen - 2
		}
	}
	for x := 0; x < b.size; x++ {
		runColor := b.modules[0][x]
		runLen := 1
		for y := 1; y < b.size; y++ {
			if b.modules[y][x] == runColor {
				runLen++
			} else {
				if runLen >= 5 {
					result += runLen - 2
				}
				runColor = b.modules[y][x]
				runLen = 1
			}
		}
		if runLen >= 5 {
			result += runLen - 2
		}
	}

	for y := 0; y < b.size-1; y++ {
		for x := 0; x < b.size-1; x++ {
			color := b.modules[y][x]
			if b.modules[y][x+1] == color && b.modules[y+1][x] == color && b.modules[y+1][x+1] == color {
				result += 3
			}
		}
	}

	for y := 0; y < b.size; y++ {
		for x := 0; x <= b.size-11; x++ {
			if finderPenaltyPattern(
				b.modules[y][x], b.modules[y][x+1], b.modules[y][x+2],
				b.modules[y][x+3], b.modules[y][x+4], b.modules[y][x+5],
				b.modules[y][x+6], b.modules[y][x+7], b.modules[y][x+8],
				b.modules[y][x+9], b.modules[y][x+10],
			) {
				result += 40
			}
		}
	}
	for x := 0; x < b.size; x++ {
		for y := 0; y <= b.size-11; y++ {
			if finderPenaltyPattern(
				b.modules[y][x], b.modules[y+1][x], b.modules[y+2][x],
				b.modules[y+3][x], b.modules[y+4][x], b.modules[y+5][x],
				b.modules[y+6][x], b.modules[y+7][x], b.modules[y+8][x],
				b.modules[y+9][x], b.modules[y+10][x],
			) {
				result += 40
			}
		}
	}

	dark := 0
	for y := 0; y < b.size; y++ {
		for x := 0; x < b.size; x++ {
			if b.modules[y][x] {
				dark++
			}
		}
	}
	total := b.size * b.size
	k := abs(dark*20-total*10) / total
	result += k * 10

	return result
}

func finderPenaltyPattern(a, b, c, d, e, f, g, h, i, j, k bool) bool {
	return (!a && !b && !c && !d && e && !f && g && h && i && !j && k) ||
		(a && !b && c && d && e && !f && g && !h && !i && !j && !k)
}

func (b *builder) setFunction(x, y int, value bool) {
	b.modules[y][x] = value
	b.isFunction[y][x] = true
}

func bit(value int, index int) bool {
	return ((value >> uint(index)) & 1) != 0
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
