// Load test: k6 run loadtest/k6.js
// Change the address with: k6 run -e BASE_URL=http://localhost:8080 loadtest/k6.js
import http from 'k6/http';
import { check, sleep } from 'k6';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const COUNTRIES = ['', 'US', 'RU', 'JP', 'GB', 'ES', 'NL'];
const QUERIES = ['', 'cat', 'doge', 'dog', 'brainrot'];

export const options = {
  stages: [
    { duration: '1m', target: 50 },  // ramp up to 50 virtual users
    { duration: '3m', target: 50 },  // hold the load; the HPA adds pods meanwhile
    { duration: '1m', target: 0 },   // ramp down; a few minutes later the HPA removes extra pods
  ],
  thresholds: {
    http_req_failed: ['rate<0.01'],   // less than 1% errors
    http_req_duration: ['p(95)<300'], // 95% of responses faster than 300 ms
  },
};

export default function () {
  const country = COUNTRIES[Math.floor(Math.random() * COUNTRIES.length)];
  const q = QUERIES[Math.floor(Math.random() * QUERIES.length)];
  const res = http.get(`${BASE}/api/memes?country=${country}&q=${encodeURIComponent(q)}`);
  check(res, { 'status 200': (r) => r.status === 200 });
  sleep(0.1);
}
