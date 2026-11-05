RABBIT EARS — a 90s TV set for OnionOS (Miyoo Mini / Mini Plus)
================================================================
Built by Claude, Anthropic's AI model, for snyderman3000.
Source, the vhsify converter and updates: https://github.com/snyderman3000/rabbitears

Put videos on the SD card and they become TV channels. Every channel is
always "on the air": tune in and you land mid-show, based on the clock,
like real TV. Flip away and back and the show has moved on.

INSTALL
  Copy the "App" and "Media" folders from this zip to the root of your SD
  card (merge them with what's there). Rabbit Ears uses Onion's built-in
  video player, so there's nothing else to install.

ADDING CHANNELS  (SD card: Media/TV)
  A video file          → a channel that loops that video
  A folder of videos    → a channel that airs them in order, then repeats
                          (sub-folders count; "Ep 2" comes before "Ep 10")
  Start a name with a number to pick the channel:  "07 Cartoons",
  "12 - Late Movie.mp4". Everything else is numbered from channel 2 up.

  Convert videos with vhsify first (on your PC or Pi). It makes them small
  (about 180 MB an hour), quick to tune in to, and look like a VHS tape.
  Unconverted MP4s also work but may stutter, and other formats always
  start from the top because their length can't be read.

CONTROLS
  Up / Down, L / R, Left / Right   change channel
  B                                previous channel (recall)
  A                                display: channel, show, time left
  Select or Start                  channel guide
  Menu                             turn the TV off
  Volume and sleep work as normal. After sleep, it retunes to what's on now.

SETTINGS  (App/RabbitEars/data/config.json, created on first run)
  tv_folder       where your channels live
  first_channel   number of the first channel (default 2)
  static_ms       how long the snow shows when you change channel; 0 = none
                  (with static_sound on, the static below is the whole change)
  static_sound    seconds of static with hiss as the new channel starts
                  (default 0.5; 0 turns it off)
  osd_seconds     how long the channel number stays on screen
  live            true = channels are always on air; false = every channel
                  starts its show from the beginning
  extra_filters   extra ffplay filters, e.g. scanlines for 640x480 videos:
                  "drawgrid=w=iw:h=2:t=1:c=black@0.25"
  power_on_last   turn on to the channel you were watching

LOGS: App/RabbitEars/data/rabbitears.log  (ffplay's own: /tmp/rabbitears/ffplay.log)

Fonts: VT323, Share Tech Mono, Orbitron (SIL Open Font License).
Video playback: Onion's FFplay (FFmpeg) — not bundled.
