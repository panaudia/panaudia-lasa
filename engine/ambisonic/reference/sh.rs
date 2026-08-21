pub fn get_spherical_harmonics(order: i32,
                               nx: f32,
                               ny: f32,
                               nz: f32,
                               gain: f32, ) -> [f32; 16] {

    let mut weights: [f32; 16] = [0.0; 16];

    const SH_0_0: f32 = 0.2820947917738781;  // Y(0,0)
    const SH_1_COEF: f32 = 0.48860251190292;  // For Y(1,-1) and Y(1,1)
    const SH_1_0: f32 = 0.4886025119029199;   // Y(1,0)
    const SH_2_COEF: f32 = 0.5462742152960395; // For Y(2,-2) and Y(2,2)
    const SH_2_1_COEF: f32 = 1.092548430592079; // For Y(2,-1) and Y(2,1)
    const SH_2_0_A: f32 = 0.9461746957575601;  // For Y(2,0)
    const SH_2_0_B: f32 = 0.31539156525252;    // For Y(2,0)
    const SH_3_COEF_F: f32 = 0.5900435899266435; // For Y(3,±3)
    const SH_3_COEF_E: f32 = 1.445305721320277; // For Y(3,±2) (multiplied by z)
    const SH_3_COEF_D_A: f32 = 2.285228997322329; // For Y(3,±1) (multiplied by z²)
    const SH_3_COEF_D_B: f32 = 0.4570457994644658; // For Y(3,±1)
    const SH_3_0_A: f32 = 1.865881662950577;   // For Y(3,0)
    const SH_3_0_B: f32 = 1.119528997770346;   // For Y(3,0)

    // Precompute commonly used values
    let nx2 = nx * nx;
    let ny2 = ny * ny;
    let nz2 = nz * nz;

    // Order 0 (omnidirectional)
    weights[0] = SH_0_0 * gain;

    // Order 1 (dipole patterns)
    let gain_1 = SH_1_COEF * gain;
    weights[1] = gain_1 * ny;
    weights[2] = SH_1_0 * nz * gain;
    weights[3] = gain_1 * nx;

    // Order 2 (quadrupole patterns)
    let fC1 = nx2 - ny2;
    let fS1 = 2.0 * nx * ny;

    let gain_2_coef = SH_2_COEF * gain;
    let gain_2_1_coef_nz = SH_2_1_COEF * nz * gain;

    weights[4] = gain_2_coef * fS1;
    weights[5] = gain_2_1_coef_nz * ny;
    weights[6] = nz2.mul_add(SH_2_0_A, -SH_2_0_B) * gain;
    weights[7] = gain_2_1_coef_nz * nx;
    weights[8] = gain_2_coef * fC1;

    // Order 3 (octupole patterns)
    if order > 2 {
        let fTmpD = nz2.mul_add(SH_3_COEF_D_A, -SH_3_COEF_D_B);
        let fTmpE = SH_3_COEF_E * nz;
        let gain_3_coef_f = SH_3_COEF_F * gain;
        let gain_3_tmp_e = fTmpE * gain;
        let gain_3_tmp_d = fTmpD * gain;

        // Y(3,-3): involves (x*sin(2φ) + y*cos(2φ)) = x*fS1 + y*fC1
        weights[9] = gain_3_coef_f * nx.mul_add(fS1, ny * fC1);
        // Y(3,-2): z * sin(2φ)
        weights[10] = gain_3_tmp_e * fS1;
        // Y(3,-1): y * (polynomial in z²)
        weights[11] = gain_3_tmp_d * ny;
        // Y(3,0): z * (polynomial in z²)
        weights[12] = nz * nz2.mul_add(SH_3_0_A, -SH_3_0_B) * gain;
        // Y(3,1): x * (polynomial in z²)
        weights[13] = gain_3_tmp_d * nx;
        // Y(3,2): z * cos(2φ)
        weights[14] = gain_3_tmp_e * fC1;
        // Y(3,3): involves (x*cos(2φ) - y*sin(2φ)) = x*fC1 - y*fS1
        weights[15] = gain_3_coef_f * nx.mul_add(fC1, -(ny * fS1));
    }
    weights
}