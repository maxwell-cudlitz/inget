// Accessors over the decoded schema: name lookups for the three list-valued blocks and
// the two derived values that would otherwise be recomputed at every call site.
package config

// EffectiveDims is the vector width that actually reaches a destination: the Matryoshka
// truncation target when one is set, otherwise the model's native width.
func (e Embedder) EffectiveDims() int {
	if e.TruncateDims > 0 {
		return e.TruncateDims
	}
	return e.Dimensions
}

// Source returns the named source, or false when no source declares that name.
func (c *Config) Source(name string) (*Source, bool) {
	for i := range c.Sources {
		if c.Sources[i].Name == name {
			return &c.Sources[i], true
		}
	}
	return nil, false
}

// Destination returns the named destination, or false when none declares that name.
func (c *Config) Destination(name string) (*Destination, bool) {
	for i := range c.Destinations {
		if c.Destinations[i].Name == name {
			return &c.Destinations[i], true
		}
	}
	return nil, false
}

// Datatype returns the named datatype, or false when none declares that name.
func (c *Config) Datatype(name string) (*Datatype, bool) {
	for i := range c.Datatypes {
		if c.Datatypes[i].Name == name {
			return &c.Datatypes[i], true
		}
	}
	return nil, false
}
