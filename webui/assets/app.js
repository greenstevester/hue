'use strict';

const toggle = document.querySelector('#automation-toggle');
const errorBox = document.querySelector('#error');
const sunriseInput = document.querySelector('#sunrise-wake');
const wakeInput = document.querySelector('#wake-override');
const bedtimeInput = document.querySelector('#bedtime-override');
const timingError = document.querySelector('#timing-error');
const phaseNames = {
  morning: 'Morning', daytime: 'Daytime', late_afternoon: 'Late afternoon',
  sunset: 'Sunset', evening: 'Evening', night: 'Night',
};
const lightErrors = new Map();
const roomRendering = new Map();
let scheduleRendering = '';
let current = null;
let isChanging = false;
let isLoading = false;
let isTimingDirty = false;

function timeText(value, zone) {
  return new Intl.DateTimeFormat('en-GB', {
    hour: '2-digit', minute: '2-digit', timeZone: zone,
  }).format(new Date(value));
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function setText(selector, value) {
  const node = document.querySelector(selector);
  if (node.textContent !== value) node.textContent = value;
}

function profileText(profile) {
  const names = { '#FF7800': 'Amber', '#FF1600': 'Deep red' };
  const colour = profile.kelvin ? `${profile.kelvin} K`
    : names[(profile.color || '').toUpperCase()] || profile.color || 'Colour';
  return `${colour} · ${profile.brightness}%`;
}

function setBusy(busy) {
  isChanging = busy;
  for (const button of document.querySelectorAll('button')) button.disabled = busy;
}

async function change(path, values) {
  const response = await fetch(path, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(values), signal: AbortSignal.timeout(8000),
  });
  if (!response.ok) throw new Error((await response.text()).trim());
  return response.json();
}

function renderLight(light, state) {
  const row = element('li', 'light');
  const info = element('div', 'light-info');
  const heading = element('div', 'light-heading');
  const dot = element('span', light.on ? 'dot on' : 'dot');
  dot.setAttribute('aria-hidden', 'true');
  heading.append(dot, element('span', '', light.name || light.id));
  info.append(heading);
  let description = light.on ? `On · ${Math.round(light.brightness)}%` : 'Off';
  if (light.on) description += light.mirek > 0
    ? ` · ${Math.round(1000000 / light.mirek / 50) * 50} K` : ' · Colour mode';
  info.append(element('p', 'light-state', description));
  if (new Date(light.hold_until) > new Date(state.at)) {
    info.append(element('p', 'hold', `Manual until ${timeText(light.hold_until, state.timezone)}`));
  }
  if (lightErrors.has(light.id)) {
    const error = element('p', 'error', lightErrors.get(light.id));
    error.setAttribute('role', 'alert');
    info.append(error);
  }
  const button = element('button', 'light-toggle', light.enabled ? 'Schedule on' : 'Schedule off');
  button.type = 'button';
  button.setAttribute('role', 'switch');
  button.setAttribute('aria-checked', String(light.enabled));
  button.setAttribute('aria-label', `Automatic lighting for ${light.name || light.id}`);
  button.title = 'Enable or disable automatic changes for this light';
  button.disabled = isChanging;
  button.addEventListener('click', async () => {
    if (isChanging) return;
    setBusy(true);
    lightErrors.delete(light.id);
    try {
      const updated = await change('/api/light', { id: light.id, enabled: !light.enabled });
      setBusy(false);
      render(updated);
    } catch (error) {
      lightErrors.set(light.id, error.message || 'Could not save this setting.');
      setBusy(false);
      render(current);
    }
  });
  row.append(info, button);
  return row;
}

function render(state) {
  current = state;
  toggle.disabled = isChanging;
  toggle.setAttribute('aria-checked', String(state.enabled));
  setText('#toggle-label', state.enabled ? 'Enabled' : 'Disabled');
  setText('#automation-description', state.enabled
    ? 'Following the daily schedule. Each light can be enabled separately.'
    : 'The schedule is paused. Your lights keep their current settings.');
  const connection = document.querySelector('#connection');
  setText('#connection', state.healthy ? 'Bridge connected' : 'Bridge reconnecting');
  connection.className = state.healthy ? 'badge connected' : 'badge';
  setText('#timezone', state.timezone);
  setText('#updated', `Updated ${timeText(state.at, state.timezone)}`);
  setText('#wake-label', state.wake_time === 'sunrise' ? 'Wake · sunrise' : 'Wake');
  setText('#wake-time', timeText(state.schedule[0].at, state.timezone));
  setText('#bedtime', state.bedtime);
  const phase = state.lights.length ? state.lights[0].target.phase : null;
  setText('#current-phase', phaseNames[phase] || 'Waiting');
  setText('#schedule-date', new Intl.DateTimeFormat('en-GB', {
    day: 'numeric', month: 'short', timeZone: state.timezone,
  }).format(new Date(state.at)));

  if (!isTimingDirty) {
    sunriseInput.checked = state.wake_time === 'sunrise';
    wakeInput.value = sunriseInput.checked ? timeText(state.schedule[0].at, state.timezone) : state.wake_time;
    wakeInput.disabled = sunriseInput.checked;
    bedtimeInput.value = state.bedtime;
  }
  for (const room of ['kitchen', 'bedroom', 'lounge']) {
    const list = document.querySelector(`#${room}-lights`);
    const lights = state.lights.filter((light) => light.room.toLowerCase() === room);
    if (room === 'lounge') list.closest('.room').hidden = !lights.length; // the lounge is optional
    const signature = JSON.stringify(lights.map((light) => [light.id, light.name, light.enabled,
      light.on, light.brightness, light.mirek, light.hold_until,
      new Date(light.hold_until) > new Date(state.at), lightErrors.get(light.id)]));
    if (roomRendering.get(room) === signature) continue;
    roomRendering.set(room, signature);
    const rows = lights.map((light) => renderLight(light, state));
    list.replaceChildren(...(rows.length ? rows : [element('li', '', 'No lights reported')]));
  }

  const signature = JSON.stringify([state.schedule, phase]);
  if (signature === scheduleRendering) return;
  scheduleRendering = signature;
  const rows = state.schedule.map((point) => {
    const row = element('li', point.phase === phase ? 'current' : '');
    const time = element('time', '', timeText(point.at, state.timezone));
    time.dateTime = point.at;
    const detail = element('div');
    detail.append(element('p', 'phase-name', phaseNames[point.phase]));
    const profiles = Object.entries(point.profiles);
    const descriptions = profiles.map(([, value]) => profileText(value));
    const description = new Set(descriptions).size === 1 ? descriptions[0]
      : profiles.map(([room, value]) => `${room}: ${profileText(value)}`).join(' · ');
    detail.append(element('p', 'phase-profile', description));
    row.append(time, detail, element('span', 'phase-marker', point.phase === phase ? 'Now' : ''));
    return row;
  });
  document.querySelector('#schedule').replaceChildren(...rows);
}

function showError(message) {
  errorBox.textContent = message;
  errorBox.hidden = !message;
}

async function refresh() {
  if (isChanging || isLoading || document.hidden) return;
  isLoading = true;
  try {
    const response = await fetch('/api/status', { signal: AbortSignal.timeout(8000) });
    if (!response.ok) throw new Error('Could not read the controller. Retrying shortly.');
    const state = await response.json();
    if (!isChanging) render(state);
    showError('');
  } catch (error) {
    document.querySelector('#connection').textContent = 'Connection lost';
    toggle.disabled = true;
    showError(error.message || 'Connection lost. Retrying shortly.');
  } finally {
    isLoading = false;
  }
}

toggle.addEventListener('click', async () => {
  if (!current || isChanging) return;
  setBusy(true);
  showError('');
  try {
    const updated = await change('/api/control', { enabled: !current.enabled });
    setBusy(false);
    render(updated);
  } catch (error) {
    setBusy(false);
    showError(error.message || 'Could not change the setting. Please try again.');
  }
});

for (const input of [sunriseInput, wakeInput, bedtimeInput]) {
  input.addEventListener('input', () => {
    isTimingDirty = true;
    wakeInput.disabled = sunriseInput.checked;
    document.querySelector('#timing-message').textContent = 'Unsaved changes';
  });
}

document.querySelector('#timing-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (isChanging) return;
  setBusy(true);
  timingError.hidden = true;
  try {
    const updated = await change('/api/schedule', {
      wake_time: sunriseInput.checked ? 'sunrise' : wakeInput.value,
      bedtime: bedtimeInput.value,
    });
    isTimingDirty = false;
    setBusy(false);
    render(updated);
    document.querySelector('#timing-message').textContent = 'Times saved';
  } catch (error) {
    setBusy(false);
    timingError.textContent = error.message || 'Could not save times.';
    timingError.hidden = false;
  }
});

document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
refresh();
setInterval(refresh, 5000);
