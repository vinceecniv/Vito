// Bridge to the CoreAudio HAL for reading and setting the input level.
// Implemented in level_darwin.m; declared here so the cgo preamble stays C.
#ifndef VITO_LEVEL_DARWIN_H
#define VITO_LEVEL_DARWIN_H

#include <stdbool.h>

// vitoInputVolumeGet writes the capture device's volume (0..1) to out.
// deviceName selects the device by name; NULL or "" means the system default.
// Returns false when the device is missing or exposes no volume control —
// plenty of USB interfaces and every aggregate device fall in that category.
bool vitoInputVolumeGet(const char *deviceName, double *out);

// vitoInputVolumeSet sets the capture device's volume (0..1).
bool vitoInputVolumeSet(const char *deviceName, double level);

#endif
