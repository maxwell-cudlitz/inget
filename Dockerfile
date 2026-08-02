# Runtime image for both binaries. Consumed by goreleaser (dockers_v2), which builds the
# binaries first and hands this file a context containing only them plus prompts/.
#
# There is no builder stage on purpose: goreleaser has already cross-compiled static
# binaries for every platform, so compiling again here would double the work and produce a
# different binary from the one in the release archives.
#
# The base is pinned by index digest rather than by tag. `nonroot` moves, and an image that
# rebuilds into a different base than the one that was tested is the failure this avoids.
# Refresh it with:
#   docker buildx imagetools inspect gcr.io/distroless/static-debian13:nonroot
FROM gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

# buildx sets TARGETPLATFORM per platform of the manifest; goreleaser lays the context out
# as <os>/<arch>/<binary> to match.
ARG TARGETPLATFORM

# Both binaries, at the paths deploy/kubernetes/ names in `command`.
COPY $TARGETPLATFORM/inget $TARGETPLATFORM/inget-fetch /usr/local/bin/

# Prompt templates are read from disk, not embedded, and config.yaml names them by relative
# path (prompts/<source>/<datatype>/<view>.md). WORKDIR is therefore load-bearing: it is
# what makes those relative paths resolve. A deployment that mounts its own prompts should
# mount them over /opt/inget/prompts.
COPY prompts /opt/inget/prompts
WORKDIR /opt/inget

# No config.yaml is baked in. The repository's copy points at localhost endpoints, which are
# wrong in every container, and a missing file fails at load naming the path it wanted —
# a better first run than silently connecting to nothing. Mount one here:
#   -v ./config.yaml:/etc/inget/config.yaml:ro
ENV INGET_CONFIG=/etc/inget/config.yaml

# uid 65532, matching runAsUser in deploy/kubernetes/. Nothing is written to disk — logs go
# to stderr, artifacts to the blob store, state to PostgreSQL — so the root filesystem can
# stay read-only.
USER nonroot:nonroot

# `docker run <image> version` reports build metadata without needing a config file.
ENTRYPOINT ["/usr/local/bin/inget"]
