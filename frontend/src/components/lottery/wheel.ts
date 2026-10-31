export const prizeColors = [
  '#0f766e',
  '#be185d',
  '#2563eb',
  '#b45309',
  '#7c3aed',
  '#047857',
  '#b91c1c',
  '#0369a1',
]

export function wheelSegments<
  T extends { id: number; weight: number; available: boolean },
>(prizes: T[]) {
  const available = prizes.filter((p) => p.available && p.weight > 0)
  const total = available.reduce((sum, p) => sum + p.weight, 0)
  let angle = 0
  return available.map((prize, i) => {
    const start = angle
    const span = (prize.weight / total) * 360
    angle += span
    return {
      prize,
      start,
      end: angle,
      center: start + span / 2,
      color: prizeColors[i % prizeColors.length],
      number: i + 1,
    }
  })
}

export function wheelRotation(current: number, center: number) {
  return current + 1440 + ((360 - center - (current % 360) + 360) % 360)
}

export function sectorPath(start: number, end: number) {
  const point = (angle: number) => {
    const r = ((angle - 90) * Math.PI) / 180
    return `${200 + 188 * Math.cos(r)},${200 + 188 * Math.sin(r)}`
  }
  if (end - start > 359.99)
    return 'M 200,12 A 188,188 0 1,1 200,388 A 188,188 0 1,1 200,12 Z'
  return `M 200,200 L ${point(start)} A 188,188 0 ${end - start > 180 ? 1 : 0},1 ${point(end)} Z`
}
