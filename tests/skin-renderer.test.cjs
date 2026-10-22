const {test}=require('node:test');
const assert=require('node:assert/strict');
const {buildMesh}=require('../internal/server/web/skin-renderer.js');
function texture(height=64){const data=new Uint8ClampedArray(64*height*4);for(let y=0;y<height;y++)for(let x=0;x<64;x++){const i=(y*64+x)*4;data.set([x,y,100,255],i);}data[3]=0;return {width:64,height,data};}
function pixel(mesh,part,face,outer=false){return mesh.find(t=>t.part===part&&t.face===face&&t.outer===outer);}
test('Minecraft front/back and outer layer UVs stay distinct',()=>{
 const mesh=buildMesh(texture());
 assert.deepEqual(pixel(mesh,'head','front').uv,[8,8]);assert.deepEqual(pixel(mesh,'head','back').uv,[24,8]);
 assert.deepEqual(pixel(mesh,'body','front').uv,[20,20]);assert.deepEqual(pixel(mesh,'leftArm','front').uv,[36,52]);
 assert.deepEqual(pixel(mesh,'head','front',true).uv,[40,8]);assert.deepEqual(pixel(mesh,'leftArm','front',true).uv,[52,52]);
 assert.deepEqual(pixel(mesh,'leftLeg','front',true).uv,[4,52]);
});
test('slim arms are 3 pixels and use the corresponding back UV offset',()=>{
 const mesh=buildMesh(texture(),'slim');
 const arm=mesh.filter(t=>t.part==='rightArm'&&t.face==='front'&&!t.outer);
 assert.equal(arm.length,36);assert.deepEqual(pixel(mesh,'rightArm','back').uv,[51,20]);
 const xs=arm.flatMap(t=>t.points.map(p=>p[0]));assert.equal(Math.max(...xs)-Math.min(...xs),3);
});
test('legacy left limbs mirror front, back and side textures',()=>{
 const mesh=buildMesh(texture(32));
 assert.deepEqual(pixel(mesh,'leftArm','front').uv,[47,20]);assert.deepEqual(pixel(mesh,'leftArm','left').uv,[43,20]);
 assert.deepEqual(pixel(mesh,'leftLeg','front').uv,[7,20]);assert.deepEqual(pixel(mesh,'leftLeg','right').uv,[11,20]);
 assert(mesh.every(t=>!t.outer||t.part==='head'));
});
test('transparent overlay pixels are skipped while base pixels remain opaque',()=>{
 const t=texture();const i=(8*64+40)*4;t.data[i+3]=0;const mesh=buildMesh(t);
 assert(!mesh.some(t=>t.outer&&t.uv[0]===40&&t.uv[1]===8));
 assert.equal(mesh.find(t=>!t.outer&&t.uv[0]===0&&t.uv[1]===8).rgba[3],255);
 t.data.fill(255);assert(buildMesh(t).every(t=>!t.outer));
});
test('reject wrong texture sizes and impossible legacy slim model',()=>{
 assert.throws(()=>buildMesh({...texture(),width:128}));assert.throws(()=>buildMesh(texture(32),'slim'));assert.throws(()=>buildMesh(texture(),'cape'));
});
