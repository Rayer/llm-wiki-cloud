---
title: Reserved DNS names for testing and documentation
source: https://www.rfc-editor.org/rfc/rfc2606.txt
---
# Public-source test data: reserved DNS names

These test notes paraphrase RFC 2606, Reserved Top Level DNS Names, by
Donald Eastlake and Aliza Panitz (June 1999). They contain no private data.
They are a small technical retrieval fixture, not operational DNS advice.

## DNS testing and documentation

RFC 2606 reserves .test for DNS software testing and .example for documentation
examples. Reserved names help keep experiments separate from real global DNS
names. Without reservations, a name used in a test might later become a real
Internet domain, creating conflicts when test software is reused.

## Invalid names and localhost

The .invalid suffix provides deliberately invalid domain names. The .localhost
reservation preserves the established association with the loopback address.
These reservations address different purposes: an intentionally invalid name
is different from a name intended to refer back to the local host.

## Example domains

RFC 2606 also lists example.com, example.net and example.org as reserved
second-level names suitable for examples. Documentation should use these
reserved example domains to reduce confusion with unrelated real domains.
