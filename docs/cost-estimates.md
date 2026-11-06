# Planning generation costs

`inget plan` reads artifacts and state without calling models or updating the
index. It reports remaining work against the current cache, rather than repeating
the cost of an initial index.
If artifacts have a backlog, the estimate covers the named oldest pending run;
`pending_artifact_runs` reports the backlog, while ingestion drains all pending runs.

The existing `estimate.cost_usd` is a conservative generation allowance: it uses
maximum output tokens and approximately four input characters per token. Input
includes rendered prompt wrappers and fragment truncation notices. Identical
fragment cache keys across items count once. The `upper_bound` label is retained
for compatibility; tokenization, retries and embedding charges mean this is not a
guaranteed ceiling on the provider bill.

Fragment calls use `models.generator` prices and output limits. View calls use the
optional `models.view_generator` role's prices and limits, falling back to the generator
when omitted. The aggregate input/output fields sum both stages; the total cost prices
each stage separately. Expected estimates validate and cap output means against the
corresponding role's budget.

An optional usage profile adds `estimate.expected`:

```sh
inget plan github/repo --estimate-profile /path/to/estimate-profile.json
```

The expected estimate applies a measured token/character relationship and mean
output tokens separately to fragments and each view. View inputs use cached
summary lengths when available, measured mean summary lengths otherwise, and the
configured composition limit, headings and separators. Actual rendered template
wrappers include item metadata. Raw-content flows use artifact byte counts as an
approximation of characters. Custom templates that condition on or repeat content
can also affect accuracy.

The profile must match the datatype and every current enricher signature. Model,
prompt or signed generation-setting changes reject stale profiles with an error.
Select a single datatype when using a profile.
It changes reporting only: signatures, cache keys, generation and ingestion
configuration are unaffected. Planning reads cached outputs without touching their
last-hit timestamps. No profile means the normal allowance alone is reported.

Signature changes do not widen the default run. To price and explicitly opt into
reprocessing every live item from a full-scope artifact, add
`--rebuild-on-signature-change` to both `inget plan` and `inget run`. A partial or
limited pass does not acknowledge the new signature; the full rebuild must succeed
before it is recorded. Profiles calibrated under the old prompts remain invalid and
must be recalibrated before they can provide `estimate.expected` again.

For a view rebuild without file-summary spending, combine the explicit rebuild flag
with `--indexed-only --cached-fragments-only`. Planning checks the selected file cache
before estimating; missing summaries fail before inference for that artifact pass.
The current fragment signature is used unless an explicit historical fallback allowlist
is supplied with `--cached-fragment-signatures`. Fallbacks still require exact fragment
paths and fingerprints and contribute their actual cached output lengths. A successful
cache-only plan reports zero fragment derivations. View generation and embedding charges
remain; embedding costs are not included in the generation estimate.

Cache-only plans also use actual cached summary lengths for the normal allowance, even
without a measured profile, including composition headings and separators. Output is still
priced at the full view budget, and tokenization remains approximate.

A profile is one JSON object, version 1, with exactly the configured view names:

```json
{
  "version": 1,
  "datatype": "github/repo",
  "fragment": {
    "signature": "sha256:CURRENT_FRAGMENT_ENRICHER_SIGNATURE",
    "samples": 100,
    "input_tokens_per_char": 0.25,
    "input_token_intercept": 20,
    "output_tokens": 45,
    "output_chars": 200,
    "prompt_chars": 460
  },
  "views": {
    "purpose": {
      "signature": "sha256:CURRENT_VIEW_ENRICHER_SIGNATURE",
      "samples": 20,
      "input_tokens_per_char": 0.25,
      "input_token_intercept": 50,
      "output_tokens": 180,
      "output_chars": 0,
      "prompt_chars": 700
    }
  }
}
```

`input_tokens_per_char` and `input_token_intercept` fit complete rendered prompt
characters to provider-reported input usage. `output_tokens` is measured mean
completion usage; fragment `output_chars` predicts composition size and must be
positive. `prompt_chars` supplies wrapper overhead for custom view enrichers that
cannot render a prompt for measurement. Omit `fragment` when fragment enrichment
is disabled. Values must be finite and nonnegative; samples must be positive.
The example illustrates the format; it does not supply usable signatures.

Expected estimates are approximate generation costs at the configured prices.
They exclude embeddings and retries, and may count views whose final composition
hash will turn out to be unchanged. Output averages can vary with repository size,
file type and model behavior. Reference-injected content and resolved reference
metadata are not modeled. Record profile provenance separately, including the
sample selection, provider usage, failures and fit quality. Paid truncated
responses belong in usage measurements even when indexing failed; the estimate
does not predict successful completion.
