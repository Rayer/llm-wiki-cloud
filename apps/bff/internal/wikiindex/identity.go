package wikiindex

import "github.com/rayer/llm-wiki-bff/internal/wikiidentity"

func ValidSyntoEntityID(value string) bool   { return wikiidentity.ValidSyntoEntityID(value) }
func ValidLegacyConceptID(value string) bool { return wikiidentity.ValidLegacyConceptID(value) }
