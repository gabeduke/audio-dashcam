# Audio Dashcam: The Passive Jam Catcher

## 🎯 Project Goal
Create a headless, always-listening audio buffer on the Raspberry Pi 5 (`fivepi`). It will passively capture the stereo master output from the Teenage Engineering USB interface. A physical pedal trigger will save the last X minutes of the jam to a permanent file, which is then automatically synced to the main Linux Mint workstation. A connected monitor will display a real-time waveform visualizer for aesthetic feedback.

## 🏗️ Hardware Architecture
*   **Core Unit:** Raspberry Pi 5 (host and user configured via `deploy.local.env`).
*   **Audio Input:** Teenage Engineering USB Interface (stereo master out from 1010 bento / MPC).
*   **Trigger:** USB Footswitch (simulating keyboard input) OR a custom GPIO button.
*   **Display:** Monitor hooked up to `fivepi` for visual feedback.
*   **Main Workstation:** Linux Mint Box (Receiver).

## 🛠️ Software Stack
*   **Audio Backend:** ALSA / PipeWire (Standard on Pi OS).
*   **Buffer / Capture Logic:** Python script using `pyaudio` and `collections.deque` (circular buffer) OR a looping `sox`/`ecasound` bash script.
*   **Trigger Listener:** Python `evdev` (for USB pedal) or `gpiozero` (for GPIO).
*   **Network Sync:** `Syncthing` (peer-to-peer sync) or an `rsync` post-save hook.
*   **Visualizer:** `cavalier` or `glava` running in kiosk mode on the Pi's monitor.

---

## 🗺️ Implementation Roadmap

### Phase 1: Connectivity & Core Audio
- [ ] Connect the Teenage Engineering interface to `fivepi` via USB.
- [ ] SSH into `fivepi` and verify the device is recognized (`arecord -l` or `lsusb`).
- [ ] Record a manual 10-second test track from the terminal to ensure clean audio capture without dropouts.

### Phase 2: The Rolling Buffer Script
- [ ] Write a Python script (`jam_catcher.py`) that constantly reads the stereo audio stream into a 10-minute memory buffer.
- [ ] Implement a "dump to file" function that writes the current buffer out to a timestamped `.wav` file (e.g., `jam_2026-09-07_1430.wav`).

### Phase 3: The Foot Pedal Integration
- [ ] Connect the digital pedal to `fivepi`.
- [ ] Add an event listener to the Python script to detect the pedal press.
- [ ] Test the full loop: Jam -> Press Pedal -> Wait for script to write the WAV file.

### Phase 4: Network Sync (The Magic Trick)
- [ ] Install `Syncthing` on both `fivepi` and the Linux Mint box.
- [ ] Share the `~/jam_saves` folder from `fivepi` to the Mint box.
- [ ] Verify that saving a file on the Pi instantly makes it appear on Mint.

### Phase 5: Visual Feedback (The Monitor)
- [ ] Install a lightweight desktop environment or window manager if running purely headless (e.g., `openbox`).
- [ ] Install a visualizer like `cavalier`.
- [ ] Hook the visualizer up to the PipeWire input stream.
- [ ] *Bonus:* Modify the Python script so the screen flashes RED for 2 seconds when the pedal is pressed to confirm the take was saved!

### Phase 6: V2 Go Rewrite (Blue/Green Deployment)
- [x] Write a feature-parity Go version of the backend (`v2-go/`) using PortAudio and a Go slice ring buffer.
- [x] Create a modern, cyberpunk "Green" HTML UI with a glowing dual-waveform visualizer.
- [x] Implement systemd-based Blue/Green toggle script (`toggle_env.sh` and `run_dashcam.sh`) to safely swap between Python (Blue) and Go (Green).
- [x] Configure both backends to serve on port `5000` so the existing NGINX proxy on port `80` can seamlessly route traffic to the active environment via `http://fivepi/`.
