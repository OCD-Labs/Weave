FROM node:20-slim

WORKDIR /app

# Copy package files
COPY package.json package-lock.json* ./

# Install dependencies
RUN npm install

# Copy source files needed for build
COPY tsconfig.json ./
COPY agent/ ./agent/

# Build TypeScript
RUN npm run build

EXPOSE 3001

CMD ["node", "dist/compose.js"]