package ingest

import "tileforge/internal/imageproc"

func HashForTest(data []byte) string { return imageproc.HashBytes(data) }
