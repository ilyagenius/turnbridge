// Shared buffer for link delivery between Swift (NE) and Go.
// Defined here so both CGo and Swift resolve to the same symbols.

char _pendingLinksStorage[8192] = {0};
char *pendingLinksPtr = _pendingLinksStorage;
int pendingLinksReady = 0;
