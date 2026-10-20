# Scope check does not stabilize the named rule

When several scope rules match, the named rule is the first exclude the same walk as scope matching hits, otherwise the first include. That walk does not prefer host over url, and it does not sort patterns. The in-scope answer is stable. The named pattern can change between calls when two excludes both match.

**Considered options:** prefer host over url, then sort patterns. Rejected because that order is not the walk scope matching uses, and that walk is what stock Compass applies.
