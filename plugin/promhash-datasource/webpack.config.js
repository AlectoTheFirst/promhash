// Minimal Grafana datasource-plugin frontend build: bundles src/module.ts
// into dist/module.js as an AMD module and copies plugin.json alongside it.
// Grafana provides the externals (react, @grafana/*) at runtime.
const path = require('path');
const CopyWebpackPlugin = require('copy-webpack-plugin');

module.exports = {
  target: 'web',
  entry: './src/module.ts',
  output: {
    path: path.resolve(__dirname, 'dist'),
    filename: 'module.js',
    libraryTarget: 'amd',
  },
  resolve: { extensions: ['.ts', '.tsx', '.js'] },
  module: {
    rules: [
      {
        test: /\.tsx?$/,
        loader: 'ts-loader',
        exclude: /node_modules/,
        options: { transpileOnly: true },
      },
    ],
  },
  externals: [
    'react',
    'react-dom',
    '@grafana/data',
    '@grafana/ui',
    '@grafana/runtime',
    '@grafana/schema',
    'rxjs',
    'lodash',
    'moment',
    '@emotion/css',
    '@emotion/react',
  ],
  plugins: [new CopyWebpackPlugin({ patterns: [{ from: 'src/plugin.json', to: '.' }] })],
};
