FROM node:20-slim

WORKDIR /app

COPY package.json package-lock.json* ./

RUN npm install

COPY tsconfig.json ./
COPY agent/ ./agent/

RUN npm run build

EXPOSE 3001

CMD ["node", "dist/compose.js"]