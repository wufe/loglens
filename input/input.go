package input

// RawLine carries a line of text plus metadata about where it came from.
type RawLine struct {
	Text   string
	Source Source
}

// Source indicates the origin of a line.
type Source int

const (
	SourceStdin  Source = iota // pipe mode
	SourceStdout              // wrapper mode — child's stdout
	SourceStderr              // wrapper mode — child's stderr
)

// InputSource produces lines and signals EOF by closing the channel.
type InputSource interface {
	Lines() <-chan RawLine
	Stop()
	ExitCode() int
}
