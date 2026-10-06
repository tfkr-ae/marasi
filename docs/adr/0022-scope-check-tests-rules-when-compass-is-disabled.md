# Scope check tests the rules when Compass is disabled

A scope check answers the live rule list even when Compass is disabled, and the result says whether Compass is enabled. Disabled Compass does not apply those rules to traffic. The check still returns the rule result so the operator can see what the rules would say.

**Considered options:** answer whether the proxy would skip or drop the request. Rejected because that hides the rule result when Compass is off, which is when the operator wants the feedback.
