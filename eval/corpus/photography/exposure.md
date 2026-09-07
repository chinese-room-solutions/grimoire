---
tags: [photography, exposure]
---

# Exposure

Three controls share one job — how much light makes the image — and they
trade against each other in stops, where one stop is a doubling or halving.

- **Aperture** — the f-number is a ratio (focal length over pupil diameter),
  so smaller numbers are bigger holes: f/1.4 admits twice the light of f/2,
  which admits twice f/2.8. The full sequence each step x1.4:
  1.4, 2, 2.8, 4, 5.6, 8, 11, 16.
- **Shutter** — halve the duration, halve the light: 1/60, 1/125, 1/250.
  Below 1/(focal length) seconds, camera shake joins the picture; below
  roughly 1/500, subject motion is what blurs.
- **ISO** — not light at all: amplification applied after capture. Doubling
  it brightens the rendering at the cost of noise and reduced dynamic range.
  The sensor captured the same photons either way.

Any two can compensate for the third, which is the whole mental model: a
stop gained here is a stop available there. Exposure compensation (`+1 EV`)
is the shorthand for "the meter got this wrong, shift it"; the camera
achieves it by whatever of the three it controls in that mode.

## What each one costs

The trade is never free, and the price is the style decision:

- Aperture buys **depth of field** — wide open, a portrait's eyes are sharp
  and the ears are not; at f/8 the whole scene is. Also, most lenses are
  their sharpest one or two stops down from wide open, and the difference
  between f/1.8 and f/4 on a cheap lens is not subtle.
- Shutter buys **motion**: freeze it or let it smear deliberately. The
  interesting failures are the middle — 1/80 on a moving child is neither
  frozen nor artfully blurred, just soft.
- ISO buys nothing. It is the control you spend when the other two are
  spent, and the question is only how much noise the camera tolerates.
  Modern sensors are good two stops past what felt usable five years ago,
  and a noisy sharp photo beats a clean blurred one every time.

## Metering and why it lies

The meter assumes the scene averages to mid-grey and exposes to make it so.
Point it at snow and it renders snow grey (underexposed); point it at a
stage-lit singer in black and it renders the singer washed out
(overexposed). The histogram is the ground truth: a landscape wants its
mass in the middle with nothing piled at either wall; piling against the
right wall is blown highlights, which carry no recoverable detail, while
the left wall is crushed blacks that mostly do.

The blinkies (highlight warning) answer the one question that matters
in-camera: did I clip? The rest is taste.

## Dynamic range and the raw advantage

The sensor captures far more range than the screen shows, and most of the
recoverable detail lives in the highlights. Base ISO holds the most range —
every ISO step up clips highlights a stop earlier, which is why "ISO
invariance" cameras still want base ISO when the light allows. Shooting
with the histogram pushed right without clipping (expose to the right)
banks highlight detail for the raw file; the JPEG preview looks washed out
and the edit restores it, see [[raw-workflow]].

Landscape past the sensor's range: a graduated filter for skies, or accept
the bracket and blend — but the cheapest fix is usually waiting ten minutes
for the light to change, which no dial on the camera offers.

## Working method

Aperture priority for almost everything: choose the depth of field, let the
camera ride the shutter, watch the shutter speed it picks and intervene
(ISO, or a wider aperture) when it drops below handholdable. Manual for
static scenes where the meter would hunt — studio, night sky, long
exposures on a tripod — and for learning, once, with the discipline of
predicting the setting before checking. The camera's guess of the scene is
a solved problem; the choice of what the picture needs is not, and no mode
dial makes it.

A stop-based head does the rest: "two stops too dark at f/8 — open to f/4
or push ISO two, and f/4's depth of field is fine for this distance" is the
whole of exposure as practised, and it is faster than any menu.
