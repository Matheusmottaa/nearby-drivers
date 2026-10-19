package driver

import (
	"fmt"
	"io"
)

// WriteMetrics writes how many drivers are available to take a ride.
func WriteMetrics(w io.Writer, available int) error {
	_, err := fmt.Fprintf(w, `# HELP driver_available Drivers with status AVAILABLE and no current ride.
# TYPE driver_available gauge
driver_available %d
`, available)
	return err
}
