// Carga sustentada de POST /routes/eta:batch no routing-service, para o HPA escalar.
// Roda dentro do cluster pelo Job em deploy/k6/job.yaml (make k8s-load ou up.ps1 -Load).
import http from 'k6/http';
import { check, sleep } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://routing:8080';
const PEAK_VUS = parseInt(__ENV.PEAK_VUS || '300', 10);
const HOLD = __ENV.HOLD || '3m';

export const options = {
  // Conexão nova a cada iteração: o kube-proxy reparte por conexão, então sem isso
  // os pods criados pelo HPA não recebem carga dos VUs que já estavam conectados.
  noVUConnectionReuse: true,
  scenarios: {
    eta: {
      executor: 'ramping-vus',
      startVUs: 1,
      stages: [
        { duration: '30s', target: 1 },
        { duration: '30s', target: PEAK_VUS },
        { duration: HOLD, target: PEAK_VUS },
        { duration: '10s', target: 1 },
        { duration: '1m', target: 1 },
      ],
      gracefulRampDown: '5s',
    },
  },
};

// Embarque e motorista da fixture de data/maps/graph.txt (nós 0 e 1).
const body = JSON.stringify({
  destination: { lat: -23.5613, lng: -46.6565 },
  origins: [{ lat: -23.5601, lng: -46.6588 }],
  max_radius_m: 15000,
});

const params = { headers: { 'Content-Type': 'application/json' }, timeout: '5s' };

export default function () {
  const res = http.post(`${BASE_URL}/routes/eta:batch`, body, params);
  check(res, { 'status 200': (r) => r.status === 200 });
  sleep(1);
}
