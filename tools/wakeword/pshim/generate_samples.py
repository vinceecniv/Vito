# train.py imports generate_samples from the old piper-sample-generator
# layout; Vito's clips come from gen_clips.py, so this only satisfies the
# import (and would generate with the current package if it were called).
from piper_sample_generator.__main__ import generate_samples  # noqa: F401
