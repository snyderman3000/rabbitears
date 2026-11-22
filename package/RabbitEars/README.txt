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
  A folder of videos    → a channel that airs them, then repeats
  Show folders inside   → each one is a show; the channel rotates between
  a channel folder        them one episode at a time (Doug ep 1, Rugrats
                          ep 1, Doug ep 2 ...). Season folders inside a
                          show stay in order ("Ep 2" before "Ep 10").
  Start a name with a number to pick the channel:  "07 Cartoons",
  "12 - Late Movie.mp4". Everything else is numbered from channel 2 up.

  Example:
    Media/TV/02 Cartoons/Doug/Season 1/Doug S01E01.mp4
    Media/TV/02 Cartoons/Rugrats/Season 1/Rugrats S01E01.mp4
    Media/TV/05 Late Movie.mp4
    Media/TV/Commercials/Crystal Pepsi.mp4
    Media/TV/Commercials/02 Cartoons/Gushers.mp4

  Show order (SETUP menu): ROTATE SHOWS, IN ORDER (every episode, folder by
  folder) or SHUFFLE. To set one channel differently, put a file called
  order.txt in its folder containing: rotate, in order or shuffle.

  Convert videos with vhsify first (on your PC or Pi). It makes them small
  (about 180 MB an hour), quick to tune in to, and look like a VHS tape.
  Unconverted MP4s also work but may stutter, and other formats always
  start from the top, with no commercial breaks, because their length
  can't be read.

COMMERCIALS  (SD card: Media/TV/Commercials)
  Put old commercials (MP4, up to 5 minutes each) in Media/TV/Commercials.
  In live TV they play before each show and in breaks during it. A folder
  inside Commercials named after a channel ("02 Cartoons" or just
  "Cartoons") holds commercials for that channel only; every other
  commercial is shared by the channels without their own.
  In SETUP: turn commercials on or off, choose how often breaks come
  (every 6 to 15 minutes, or only between shows) and how many commercials
  play in each break. Breaks are part of the schedule, so the guide's times
  stay right.

DVR MODE  (SETUP > MODE > DVR)
  Instead of live channels, pick any channel, show, season and episode
  and watch it from the start. Rabbit Ears remembers where you stopped and
  picks up from there, plays the next episode of the same show when one
  ends, and marks what you've watched. No commercials in DVR mode.

CONTROLS — LIVE TV
  Up / Down, L / R, Left / Right   change channel
  B                                previous channel (recall)
  A                                display: channel, show, time left
  Select                           channel guide
  Start                            SETUP menu
  Menu                             turn the TV off
  Volume and sleep work as normal. After sleep, it retunes to what's on now.

CONTROLS — DVR LIST
  A          open a folder / play (resumes where you stopped)
  Y          play from the beginning
  X          mark watched / unwatched (a whole folder at once)
  B          back up a folder
  Select     back to live TV
  Start      SETUP menu        Menu   turn the TV off

CONTROLS — WATCHING IN DVR MODE  (Onion's video player)
  Left / Right  -/+10 seconds     Up / Down  +/-1 minute
  Y / X         -/+10 minutes     A          pause
  Menu          stop and go back to the list

SETTINGS  (App/RabbitEars/data/config.json, created on first run; the
SETUP menu changes the first five)
  mode            "live" or "dvr"
  show_order      "rotate", "in order" or "shuffle"
  commercials     true or false
  ads_every_min   minutes between breaks inside a show (0 = between shows)
  ads_per_break   commercials in each break
  tv_folder       where your channels live
  first_channel   number of the first channel (default 2)
  static_ms       how long the snow shows when you change channel
  osd_seconds     how long the channel number stays on screen
  extra_filters   extra ffplay filters, e.g. scanlines for 640x480 videos:
                  "drawgrid=w=iw:h=2:t=1:c=black@0.25"
  power_on_last   turn on to the channel you were watching

LOGS: App/RabbitEars/data/rabbitears.log  (ffplay's own: /tmp/rabbitears/ffplay.log)

Fonts: VT323, Share Tech Mono, Orbitron (SIL Open Font License).
Video playback: Onion's FFplay (FFmpeg) — not bundled.
