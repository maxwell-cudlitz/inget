// The fetch command: enumerate a source's items, fragment what changed, commit one artifact run.
//
// Flags follow the design's CLI surface. Three of them force partial scope, because a run that
// looked at a caller-supplied subset cannot tell "absent from the source" from "not asked about",
// and a tombstone issued on that basis would delete a live item.
//
// The report goes to stdout as JSON, because stdout carries program data; everything diagnostic is
// a log line on stderr.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/maxwellcudlitz/inget/internal/artifact"
	"github.com/maxwellcudlitz/inget/internal/cli"
	"github.com/maxwellcudlitz/inget/internal/config"
	"github.com/maxwellcudlitz/inget/internal/fetch"
	"github.com/maxwellcudlitz/inget/internal/source"
)

// fetchOptions are the command's flags.
type fetchOptions struct {
	source    string
	datatype  string
	only      []string
	eventFile string
	since     string
	scope     string
	limit     int
	dryRun    bool
}

// register attaches the flags to a flag set. The receiver is a pointer because cobra binds
// directly into these fields.
func (o *fetchOptions) register(flags *pflag.FlagSet) {
	flags.StringVar(&o.source, "source", "", "restrict to one configured source (default: all)")
	flags.StringVar(&o.datatype, "datatype", "", "restrict to one datatype (default: all for the source)")
	flags.StringSliceVar(&o.only, "only", nil, "explicit item IDs; forces --scope partial")
	flags.StringVar(&o.eventFile, "event-file", "", "webhook payload naming the items to fetch; forces --scope partial")
	flags.StringVar(&o.since, "since", "", "only items changed since this RFC 3339 timestamp, date, or duration ago (e.g. 24h)")
	flags.StringVar(&o.scope, "scope", string(artifact.ScopeFull), "full or partial; partial suppresses tombstones")
	flags.IntVar(&o.limit, "limit", 0, "stop after this many items")
	flags.BoolVar(&o.dryRun, "dry-run", false, "enumerate and report; write nothing")
}

// runFetch resolves the datatypes named on the command line and fetches each in turn.
func runFetch(cmd *cobra.Command, opts fetchOptions) error {
	cfg, err := config.Load(cli.ConfigPath(cmd))
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	since, err := parseSince(opts.since)
	if err != nil {
		return err
	}
	scope := artifact.Scope(opts.scope)
	if scope != artifact.ScopeFull && scope != artifact.ScopePartial {
		return fmt.Errorf("--scope %q, want %s or %s", opts.scope, artifact.ScopeFull, artifact.ScopePartial)
	}
	targets, err := selectDatatypes(cfg, opts.source, opts.datatype)
	if err != nil {
		return err
	}

	reports := make([]*fetch.Report, 0, len(targets))
	for _, dt := range targets {
		report, err := fetchOne(cmd.Context(), cfg, dt, opts, scope, since)
		if err != nil {
			return err
		}
		reports = append(reports, report)
	}
	return writeJSON(cmd.OutOrStdout(), reports)
}

// fetchOne runs one datatype.
func fetchOne(ctx context.Context, cfg *config.Config, dt config.Datatype, opts fetchOptions,
	scope artifact.Scope, since time.Time) (*fetch.Report, error) {
	deps, cleanup, err := buildDeps(ctx, cfg, dt)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	only := opts.only
	if opts.eventFile != "" {
		ids, err := itemIDsFromEvent(deps.Connector, opts.eventFile)
		if err != nil {
			return nil, err
		}
		only = append(only, ids...)
	}

	domainHash, err := cfg.DomainHash(dt.Source)
	if err != nil {
		return nil, err
	}
	configHash, err := cfg.ConfigHash(dt.Source, dt.Name)
	if err != nil {
		return nil, err
	}
	return fetch.Run(ctx, deps, fetch.RunConfig{
		Source:              dt.Source,
		Datatype:            dt.Name,
		Scope:               scope,
		Only:                only,
		Since:               since,
		Limit:               opts.limit,
		DryRun:              opts.dryRun,
		Concurrency:         concurrency(deps.Connector),
		MaxFragmentsPerItem: cfg.Artifacts.MaxFragmentsPerItem,
		DomainHash:          domainHash,
		ConfigHash:          configHash,
		Producer:            producer(),
	})
}

// selectDatatypes resolves the --source and --datatype filters to the datatypes to fetch.
//
// A datatype named explicitly must exist and must be fetchable, so an unregistered driver is an
// error. A datatype merely swept up by the default "everything" is skipped with a warning instead:
// the shipped configuration declares sources whose connectors land in later steps, and failing the
// whole command over one of those would make the implemented half unusable.
func selectDatatypes(cfg *config.Config, sourceName, datatypeName string) ([]config.Datatype, error) {
	if datatypeName != "" {
		dt, ok := cfg.Datatype(datatypeName)
		if !ok {
			return nil, fmt.Errorf("unknown datatype: %s", datatypeName)
		}
		if sourceName != "" && dt.Source != sourceName {
			return nil, fmt.Errorf("datatype %s reads source %s, not %s", dt.Name, dt.Source, sourceName)
		}
		if !fetchable(cfg, *dt) {
			return nil, fmt.Errorf("datatype %s reads source %s, whose driver is not built into this binary (registered: %v)",
				dt.Name, dt.Source, source.Registered())
		}
		return []config.Datatype{*dt}, nil
	}
	if sourceName != "" {
		if _, ok := cfg.Source(sourceName); !ok {
			return nil, fmt.Errorf("unknown source: %s", sourceName)
		}
	}
	var out []config.Datatype
	for _, dt := range cfg.Datatypes {
		if sourceName != "" && dt.Source != sourceName {
			continue
		}
		if !fetchable(cfg, dt) {
			slog.Warn("skipping datatype whose source driver is not built into this binary",
				"datatype", dt.Name, "source", dt.Source, "registered", source.Registered())
			continue
		}
		out = append(out, dt)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no configured datatype has a fetchable source (registered drivers: %v)", source.Registered())
	}
	return out, nil
}

// fetchable reports whether a datatype's source names a driver this binary links.
func fetchable(cfg *config.Config, dt config.Datatype) bool {
	src, ok := cfg.Source(dt.Source)
	if !ok {
		return false
	}
	return slices.Contains(source.Registered(), src.Driver)
}

// itemIDsFromEvent reads a webhook payload and asks the connector what items it names.
func itemIDsFromEvent(conn source.Connector, path string) ([]string, error) {
	mapper, ok := conn.(source.EventMapper)
	if !ok {
		return nil, fmt.Errorf("source %s cannot map webhook payloads", conn.Name())
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading event file %s: %w", path, err)
	}
	ids, err := mapper.ItemIDsFromEvent(payload)
	if err != nil {
		return nil, fmt.Errorf("mapping event file %s: %w", path, err)
	}
	return ids, nil
}

// parseSince accepts an RFC 3339 timestamp, a plain date, or a duration meaning "that long ago".
// The duration form exists because "--since 24h" is what an operator running a nightly job
// actually wants, and computing the timestamp in a shell wrapper is a step nobody should need.
func parseSince(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if when, err := time.Parse(time.RFC3339, raw); err == nil {
		return when.UTC(), nil
	}
	if when, err := time.Parse(time.DateOnly, raw); err == nil {
		return when.UTC(), nil
	}
	if ago, err := time.ParseDuration(raw); err == nil && ago > 0 {
		return time.Now().UTC().Add(-ago), nil
	}
	return time.Time{}, fmt.Errorf("--since %q: want an RFC 3339 timestamp, a YYYY-MM-DD date, or a duration such as 24h", raw)
}

// concurrency asks the connector how many items it tolerates in flight, falling back to the
// package default. The number belongs to the source: it is the third-party API's tolerance.
func concurrency(conn source.Connector) int {
	if limited, ok := conn.(interface{ Concurrency() int }); ok {
		return limited.Concurrency()
	}
	return 0
}

// producer is the name/version string the manifest records.
func producer() string { return "inget-fetch/" + cli.Build().Version }

// writeJSON renders v to w as indented JSON followed by a newline.
func writeJSON(w io.Writer, v any) error {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding report: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", encoded); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}
	return nil
}
