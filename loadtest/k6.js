// Нагрузочный тест: k6 run loadtest/k6.js
// Адрес можно поменять: k6 run -e BASE_URL=http://localhost:8080 loadtest/k6.js
import http from 'k6/http';
import { check, sleep } from 'k6';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const COUNTRIES = ['', 'US', 'RU', 'JP', 'GB', 'ES', 'NL'];
const QUERIES = ['', 'кот', 'doge', 'ждун', 'cat'];

export const options = {
  stages: [
    { duration: '1m', target: 50 },  // плавно разгоняемся до 50 «пользователей»
    { duration: '3m', target: 50 },  // держим нагрузку — в это время HPA добавляет поды
    { duration: '1m', target: 0 },   // снижаем — через несколько минут HPA уберёт лишние поды
  ],
  thresholds: {
    http_req_failed: ['rate<0.01'],   // меньше 1% ошибок
    http_req_duration: ['p(95)<300'], // 95% ответов быстрее 300 мс
  },
};

export default function () {
  const country = COUNTRIES[Math.floor(Math.random() * COUNTRIES.length)];
  const q = QUERIES[Math.floor(Math.random() * QUERIES.length)];
  const res = http.get(`${BASE}/api/memes?country=${country}&q=${encodeURIComponent(q)}`);
  check(res, { 'status 200': (r) => r.status === 200 });
  sleep(0.1);
}
