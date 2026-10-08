// Shared progress wiring keeps terminal output optional and separate from stdout data.
// The command owns the display, while worker code sees only a context observer.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/logging"
	"github.com/maxwell-cudlitz/inget/internal/progress"
)

// ProgressFlag selects a human display without changing configuration or cache keys.
const ProgressFlag = "progress"

func configureProgress(cmd *cobra.Command) error {
	opts, err := config.LoadLog(ConfigPath(cmd))
	if err != nil {
		return fmt.Errorf("reading log configuration: %w", err)
	}
	mode := cmd.Root().PersistentFlags().Lookup(ProgressFlag).Value.String()
	display, err := progress.New(cmd.ErrOrStderr(), mode)
	if err != nil {
		return err
	}
	if display == nil {
		_, err = logging.Setup(opts)
	} else {
		_, err = logging.SetupWithStderr(opts, display)
	}
	if err != nil {
		if display != nil {
			display.Close()
		}
		return fmt.Errorf("configuring logger: %w", err)
	}
	if display != nil {
		ctx := progress.WithObserver(cmd.Context(), display)
		cmd.Root().SetContext(ctx)
		cmd.SetContext(ctx)
	}
	return nil
}
