# Repository view prompts

This profile has seven separately embedded repository views. The narrative prompts
preserve concrete names, responsibilities, relationships and workflows, using the length
supported by the evidence rather than a word or paragraph target. Each has short, medium
and long examples. Search terms remain bounded at 32 distinct phrases, six words each,
to keep the output complete and focused.

The local Vertex configuration uses a separate `models.view_generator`: 1,792 output
tokens, 500,000 input characters, and a 437,500-character composed document. File
generation keeps its original model settings, 1,024-token budget and 4,000-character
file prefix. The fragment prompt is deliberately unchanged, preserving its cache keys.

The view budget leaves some room under `text-embedding-005`'s 2,048-token input limit.
Generation and embedding tokenizers can differ, so it is not a mathematical guarantee:
the local bridge sends complete text with `autoTruncate: false` and rejects reported
truncation. Longer text requires an embedder with sufficient context or a separately
designed multi-vector representation.

The model limit and `autoTruncate` behavior are documented in Google's
[text embeddings guide](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/embeddings/get-text-embeddings).

The larger composition budget reduces omitted file summaries, but input filters,
per-file truncation and composition limits still bound the evidence. These prompts ask
for grounded descriptions and explicit uncertainty when evidence is missing. Changing
prompts does not automatically expand the item work set; opt into a rebuild explicitly.

After an existing ingestion process finishes, `python3 .inget/local/local.py restart-bridge`
reloads the adapter without restarting PostgreSQL. A running ingestion process keeps its
original binary, prompts and model settings; new settings take effect on its next invocation.
