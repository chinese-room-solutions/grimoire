---
tags: [photography, raw, workflow]
---

# Raw workflow

A raw file is the sensor data plus the metadata: the photosites' values
before any of the decisions — white balance, tone curve, sharpening,
demosaicing — have been baked in. The camera renders JPEGs from it
immediately and throws most of the information away; keeping the raw file
is keeping the negative.

## What the extra data buys

- **White balance is a tag, not a bake.** The white balance applied to
  JPEGs in-camera is a colour transform fixed at capture time; on the raw
  file it is a setting you change freely, which is the difference between
  "shot at 3200K" being a commitment and a suggestion.
- **Highlight headroom.** The raw file holds roughly a stop or two of
  recoverable highlight detail past where the histogram's right wall sits,
  because the histogram previews the rendered image, not the capture — the
  exposure logic in [[exposure#Dynamic range and the raw advantage]] banks
  on this.
- **Bit depth.** 12-14 bits per channel against 8, which sounds academic
  until an edit pushes a shadow three stops and the 8-bit version bands
  while the raw version stays smooth. Heavy edits are where the margin
  is spent.
- **Re-editability.** The edits are instructions stored beside the file, so
  last year's photos re-render with this year's (better) demosaicer for
  free. JPEGs edited destructively five years ago stay exactly as wrong
  as they were made.

## The pipeline

1. **Copy cards to disk, verify, only then format in camera.** The raw
   files are the negatives; they go into the backup set like any other
   irreplaceable data, [[backups#The rule]]. Two copies on different
   devices before the card is reused, every time, no exceptions for a
   good day.
2. **Cull ruthlessly before editing.** Flag picks, reject the technically
   dead (camera shake, clipped, missed focus), and edit the few dozen that
   survive. Time spent perfecting a photo that will not be shown is time
   not spent on the ones that will.
3. **Edit**: white balance, exposure, then locals (dodge/burn, gradient for
   skies). Global-first, local-second, and stop earlier than instinct says
   — the taste failure of raw's latitude is the over-edited HDR look that
   a tighter medium enforced against.
4. **Export**: JPEGs at quality ~85 for sharing, sRGB because that is what
   every screen assumes. Full-size for print, resized for the web.

Sidecars carry the edits — XMP for camera raws, or a catalogue database
(Lightroom, darktable's library) that stores them centrally. The catalogue
is a single point of failure that must itself be backed up, and its
lock-file is why two machines cannot share one catalogue over NFS without
corruption theatre.

## Formats and compatibility

Every vendor has a raw flavour (CR3, NEF, ARW...) that only their software
reads promptly and that support in open tooling arrives late — a real
archival problem, since the files outlive the vendor's interest. Two
mitigations: DNG, Adobe's open container, converts at import and embeds the
original if asked; or shoot lossless-compressed vendor raw and accept that
in ten years darktable will read it fine, because it always has so far.
Lossy-compressed raws (the small options on some bodies) are for buffer
depth and card space, and I do not use them: compressing the negative
before developing it inverts the whole point.

EXIF travels inside the file — lens, shutter, aperture, GPS — and survives
edits that would strip it from exports only if the exporter is told to
preserve it. The GPS part is worth a deliberate decision, not a default.

## Where JPEGs are correct

Straight out of camera, the rendering is genuinely good, and for anything
that is captured, shared, and forgotten — snapshots of the whiteboard,
eBay listings — shooting raw is storage overhead and nothing else. The
camera's pipeline is also faster than any laptop's. The honest division:
raw when the light is difficult or the photo matters, JPEGs when neither,
and the discipline is knowing which one you are taking *before* pressing,
because the file type is decided at capture and no amount of workflow
recovers a negative that was never kept.
