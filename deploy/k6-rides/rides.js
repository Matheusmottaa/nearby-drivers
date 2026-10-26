// Corridas de verdade: DRIVERS motoristas com heartbeat e PASSENGERS solicitantes.
// Cada pedido passa por ride -> matching -> routing, então aparece no painel de
// corridas e mexe na CPU de matching e routing. O k6 publica k6_http_reqs_total,
// que é a série do painel de requisições.
//
//   DRIVERS=30 PASSENGERS=8 HOLD=3m HEARTBEAT_S=5 ASSIGNED_HOLD_S=10
//   make k8s-rides DRIVERS=30 PASSENGERS=8
//   .\deploy\k8s\up.ps1 -Rides -Drivers 30 -Passengers 8
import http from 'k6/http';
import { check, sleep } from 'k6';

const RIDE_URL = __ENV.RIDE_URL || 'http://ride:8080';
const DRIVER_URL = __ENV.DRIVER_URL || 'http://driver:8080';
const DRIVERS = parseInt(__ENV.DRIVERS || '30', 10);
const PASSENGERS = parseInt(__ENV.PASSENGERS || '8', 10);
const HOLD = __ENV.HOLD || '3m';
const HEARTBEAT_S = Number(__ENV.HEARTBEAT_S || '5');
const ASSIGNED_HOLD_S = Number(__ENV.ASSIGNED_HOLD_S || '10');

const PICKUP = { lat: -19.9226633, lng: -43.9444382 };
// Nós de data/maps/graph.txt perto da Praça Raul Soares, dentro do raio de 3 km.
const SPOTS = [
  { lat: -19.9221597, lng: -43.9455038 },
  { lat: -19.9237873, lng: -43.9455308 },
  { lat: -19.9232361, lng: -43.9438881 },
  { lat: -19.9215947, lng: -43.9433979 },
  { lat: -19.920269, lng: -43.9445353 },
  { lat: -19.9220458, lng: -43.9476755 },
  { lat: -19.9240106, lng: -43.9475387 },
  { lat: -19.9246041, lng: -43.9428877 },
  { lat: -19.925442, lng: -43.9457936 },
  { lat: -19.9222332, lng: -43.9417274 },
  { lat: -19.9198466, lng: -43.9468618 },
  { lat: -19.9206022, lng: -43.9485607 },
];

const jsonHeaders = { headers: { 'Content-Type': 'application/json' }, timeout: '10s' };

const scenarios = {
  rides: {
    executor: 'constant-vus',
    vus: PASSENGERS,
    duration: HOLD,
    exec: 'requestRide',
    gracefulStop: '20s',
  },
};
if (DRIVERS > 0) {
  scenarios.heartbeats = {
    executor: 'constant-vus',
    vus: DRIVERS,
    duration: HOLD,
    exec: 'heartbeat',
    gracefulStop: '5s',
  };
}

export const options = {
  scenarios,
  setupTimeout: '3m',
};

export function setup() {
  if (PASSENGERS < 1) {
    throw new Error('PASSENGERS tem que ser >= 1');
  }
  if (DRIVERS < 0) {
    throw new Error('DRIVERS tem que ser >= 0');
  }
  const run = Date.now().toString(36);
  const drivers = [];
  for (let i = 0; i < DRIVERS; i++) {
    const spot = SPOTS[i % SPOTS.length];
    const plate = `K${run}${i}`;
    const created = http.post(
      `${DRIVER_URL}/drivers`,
      JSON.stringify({
        name: `k6 driver ${i}`,
        phone: `+55119${String(i).padStart(8, '0')}`,
        vehicle_class: 'standard',
        plate,
      }),
      {
        headers: {
          'Content-Type': 'application/json',
          'Idempotency-Key': `k6-driver-${run}-${i}`,
        },
        timeout: '10s',
        tags: { name: 'POST /drivers' },
      },
    );
    if (created.status !== 201 && created.status !== 200) {
      throw new Error(`POST /drivers ${i} -> ${created.status} ${created.body}`);
    }
    const id = created.json('id');
    const available = http.patch(
      `${DRIVER_URL}/drivers/${id}/status`,
      JSON.stringify({ status: 'AVAILABLE' }),
      Object.assign({ tags: { name: 'PATCH /drivers/status' } }, jsonHeaders),
    );
    if (available.status !== 200) {
      throw new Error(`PATCH status ${id} -> ${available.status} ${available.body}`);
    }
    const fix = location(id, spot, 1);
    if (fix.status !== 204) {
      throw new Error(`PUT location ${id} -> ${fix.status} ${fix.body}`);
    }
    drivers.push({ id, lat: spot.lat, lng: spot.lng });
  }
  return { drivers };
}

export function heartbeat(data) {
  // __VU é global aos dois cenários. O índice direto caía fora da lista,
  // a função voltava sem sleep e o k6 estourava a memória.
  const driver = data.drivers[(__VU - 1) % data.drivers.length];
  if (!driver) {
    sleep(HEARTBEAT_S);
    return;
  }
  const seq = (__ITER + 2);
  const res = location(driver.id, driver, seq);
  check(res, { 'heartbeat 204': (r) => r.status === 204 });
  sleep(HEARTBEAT_S);
}

let passengerID;

export function requestRide() {
  if (!passengerID) {
    passengerID = uuid();
  }
  const key = uuid();
  const created = http.post(
    `${RIDE_URL}/rides`,
    JSON.stringify({
      passenger_id: passengerID,
      pickup: PICKUP,
      vehicle_class: 'standard',
    }),
    {
      headers: {
        'Content-Type': 'application/json',
        'Idempotency-Key': key,
      },
      timeout: '10s',
      tags: { name: 'POST /rides' },
    },
  );
  const accepted = check(created, { 'ride 202': (r) => r.status === 202 });
  if (!accepted) {
    sleep(1);
    return;
  }
  const id = created.json('id');
  let status = created.json('status');
  for (let i = 0; i < 20 && (status === 'REQUESTED' || status === 'MATCHING'); i++) {
    const got = http.get(`${RIDE_URL}/rides/${id}?wait=3s`, {
      timeout: '8s',
      tags: { name: 'GET /rides/{id}' },
    });
    if (got.status !== 200) {
      break;
    }
    status = got.json('status');
  }
  check(status, {
    'corrida fechou': (s) => s === 'DRIVER_ASSIGNED' || s === 'NO_DRIVER_AVAILABLE',
  });
  if (status === 'DRIVER_ASSIGNED' && ASSIGNED_HOLD_S > 0) {
    sleep(ASSIGNED_HOLD_S);
    const cancelled = http.post(
      `${RIDE_URL}/rides/${id}/cancel`,
      JSON.stringify({ reason: 'k6' }),
      Object.assign({ tags: { name: 'POST /rides/cancel' } }, jsonHeaders),
    );
    check(cancelled, { 'cancel 200': (r) => r.status === 200 });
  }
  sleep(1);
}

function location(id, spot, seq) {
  return http.put(
    `${DRIVER_URL}/drivers/${id}/location`,
    JSON.stringify({
      lat: spot.lat,
      lng: spot.lng,
      seq,
      observed_at: new Date().toISOString(),
      accuracy_m: 5,
    }),
    Object.assign({ tags: { name: 'PUT /drivers/location' } }, jsonHeaders),
  );
}

function uuid() {
  const bytes = new Uint8Array(16);
  for (let i = 0; i < 16; i++) {
    bytes[i] = Math.floor(Math.random() * 256);
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
