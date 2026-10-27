// Genera los íconos del lanzador (uno por estilo) desde los mismos estilos que usa la app, y los
// "alias" del manifiesto que permiten cambiarlo (Android solo deja elegir entre íconos incluidos).
// Uso: node gen-iconos.mjs <código de Hilvan> <carpeta de salida>
import { writeFileSync, mkdirSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { join } from 'node:path';

const [src, out] = process.argv.slice(2);
const { ICON_STYLES, iconSVG } = await import(join(src, 'web/js/appicon-styles.js'));
mkdirSync(join(out, 'res/mipmap-xxxhdpi'), { recursive: true });
mkdirSync(join(out, 'res/mipmap-anydpi-v26'), { recursive: true });
mkdirSync(join(out, 'svg'), { recursive: true });
const aliases = [];
for (const st of ICON_STYLES) {
  if (st.id !== 'classic') {
    const svg = join(out, 'svg', `${st.id}.svg`);
    writeFileSync(svg, iconSVG({ ...st, launcher: true }));
    execFileSync('rsvg-convert', ['-w', '432', '-h', '432', svg, '-o', join(out, `res/mipmap-xxxhdpi/ic_st_${st.id}.png`)]);
    writeFileSync(join(out, `res/mipmap-anydpi-v26/ic_st_${st.id}_a.xml`), `<?xml version="1.0" encoding="utf-8"?>
<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">
    <background android:drawable="@mipmap/ic_st_${st.id}" />
    <foreground android:drawable="@drawable/transparente" />
</adaptive-icon>
`);
  }
  const icon = st.id === 'classic' ? '@mipmap/ic_launcher' : `@mipmap/ic_st_${st.id}_a`;
  aliases.push(`        <activity-alias
            android:name=".Icono_${st.id}"
            android:targetActivity=".MainActivity"
            android:icon="${icon}"
            android:roundIcon="${icon}"
            android:label="Hilvan"
            android:enabled="${st.id === 'classic'}"
            android:exported="true">
            <intent-filter>
                <action android:name="android.intent.action.MAIN" />
                <category android:name="android.intent.category.LAUNCHER" />
            </intent-filter>
        </activity-alias>`);
}
writeFileSync(join(out, 'alias.xml'), aliases.join('\n'));
console.log(`${ICON_STYLES.length} íconos`);
